// Command scoreaudit compares the scoring engine's ESPN points with ESPN's own
// per-player points for the owner's leagues. Run locally: it needs ESPN cookies.
//
//	go run ./cmd/scoreaudit -season 2026 -weeks 1-3 -league espn:123456 [-league ...] [-v]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vsnandy/sports-api-go/internal/domain"
	"github.com/vsnandy/sports-api-go/internal/players"
	"github.com/vsnandy/sports-api-go/internal/providers/espn"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
	"github.com/vsnandy/sports-api-go/internal/providers/sleeper"
	"github.com/vsnandy/sports-api-go/internal/scoreaudit"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var leagues multiFlag
	season := flag.Int("season", 0, "NFL season (required)")
	weeks := flag.String("weeks", "", "week or range, e.g. 3 or 1-3 (required)")
	threshold := flag.Float64("threshold", 0.98, "minimum exact rate to pass")
	verbose := flag.Bool("v", false, "print each mismatch example's Sleeper stat line")
	flag.Var(&leagues, "league", "ESPN league like espn:123456 (repeatable; default ESPN_LEAGUE_IDS)")
	flag.Parse()

	ok, err := run(context.Background(), *season, *weeks, *threshold, *verbose, leagues)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scoreaudit:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func run(ctx context.Context, season int, weeksArg string, threshold float64, verbose bool, leagueFlags []string) (bool, error) {
	if season < 2000 {
		return false, errors.New("-season is required")
	}
	weeks, err := parseWeeks(weeksArg)
	if err != nil {
		return false, err
	}
	natives, err := leagueIDs(leagueFlags, os.Getenv("ESPN_LEAGUE_IDS"))
	if err != nil {
		return false, err
	}
	s2, swid, err := cookies(ctx)
	if err != nil {
		return false, err
	}

	ec := espn.New(httpx.New("espn", 30*time.Second), espn.DefaultBase, s2, swid, natives)
	sl := sleeper.New(httpx.New("sleeper", 60*time.Second), sleeper.DefaultAPIBase, sleeper.DefaultStatsBase, "")
	idx := players.New(sl, players.NoStore{}, time.Now)

	scorings := map[string]domain.Scoring{}
	lines := map[int]map[string]domain.StatLine{}
	var samples []scoreaudit.Sample
	for _, native := range natives {
		lg, err := ec.League(ctx, native, season)
		if err != nil {
			return false, fmt.Errorf("league %s: %w", native, err)
		}
		scorings[lg.ID] = lg.ScoringModel()
		for _, w := range weeks {
			if _, ok := lines[w]; !ok {
				if lines[w], err = sl.WeekStats(ctx, season, w); err != nil {
					return false, fmt.Errorf("sleeper week %d stats: %w", w, err)
				}
			}
			pts, err := ec.WeekPlayerPoints(ctx, native, season, w)
			if err != nil {
				return false, fmt.Errorf("league %s week %d: %w", native, w, err)
			}
			for _, pp := range pts {
				p, ok, err := idx.Resolve(ctx, pp.Ref)
				if err != nil {
					return false, err
				}
				s := scoreaudit.Sample{League: lg.ID, Week: w, Name: pp.Ref.Name, Mapped: ok, Total: pp.Total, ByStat: pp.ByStat}
				if ok {
					s.Position = p.Position
					s.Stats = lines[w][*p.ID].Stats
				}
				samples = append(samples, s)
			}
		}
	}

	r := scoreaudit.Compare(samples, scorings, espn.ESPNStatIDForKey)
	fmt.Printf("compared %d player-weeks · exact %d (%.1f%%) · unmapped %d (excluded)\n",
		r.Compared, r.Exact, 100*r.ExactRate(), r.Unmapped)
	if len(r.Mismatches) > 0 {
		fmt.Println("top mismatches:")
	}
	for i, m := range r.Mismatches {
		if i == 10 {
			break
		}
		label := fmt.Sprintf("espn stat %d", m.StatID)
		if m.StatID == 0 {
			label = "engine-only keys"
		}
		fmt.Printf("  %-16s x%-4d expected %+.2f got %+.2f  e.g. %s\n", label, m.Count, m.Expected, m.Got, m.Example)
		if verbose {
			fmt.Printf("      sleeper stats: %v\n", m.ExampleStats)
		}
	}
	pass := r.Pass(threshold)
	if pass {
		fmt.Printf("PASS (>= %.0f%%)\n", 100*threshold)
	} else {
		fmt.Printf("FAIL (< %.0f%%)\n", 100*threshold)
	}
	return pass, nil
}

// parseWeeks accepts "N" or "N-M" with 1 <= N <= M <= 18.
func parseWeeks(s string) ([]int, error) {
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	a, errA := strconv.Atoi(lo)
	b, errB := strconv.Atoi(hi)
	if errA != nil || errB != nil || a < 1 || b > 18 || a > b {
		return nil, fmt.Errorf("-weeks must be N or N-M within 1-18, got %q", s)
	}
	var out []int
	for w := a; w <= b; w++ {
		out = append(out, w)
	}
	return out, nil
}

// leagueIDs returns native ESPN league IDs from -league flags, else ESPN_LEAGUE_IDS.
func leagueIDs(flags []string, env string) ([]string, error) {
	var out []string
	for _, f := range flags {
		p, native, err := domain.ParseLeagueID(f)
		if err != nil || p != domain.PlatformESPN {
			return nil, fmt.Errorf("-league %q must look like espn:<digits>", f)
		}
		out = append(out, native)
	}
	if len(out) == 0 {
		for _, id := range strings.Split(env, ",") {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no leagues: pass -league espn:<id> or set ESPN_LEAGUE_IDS")
	}
	return out, nil
}

// cookies returns ESPN_S2/ESPN_SWID from the environment, else from SSM.
func cookies(ctx context.Context) (string, string, error) {
	if s2, swid := os.Getenv("ESPN_S2"), os.Getenv("ESPN_SWID"); s2 != "" && swid != "" {
		return s2, swid, nil
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return "", "", err
	}
	const s2Name, swidName = "/sports-api/espn-s2", "/sports-api/espn-swid"
	out, err := ssm.NewFromConfig(cfg).GetParameters(ctx, &ssm.GetParametersInput{
		Names: []string{s2Name, swidName}, WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", "", fmt.Errorf("reading ESPN cookies from SSM: %w", err)
	}
	vals := map[string]string{}
	for _, p := range out.Parameters {
		vals[aws.ToString(p.Name)] = aws.ToString(p.Value)
	}
	if vals[s2Name] == "" || vals[swidName] == "" {
		return "", "", errors.New("ESPN cookies not found (set ESPN_S2/ESPN_SWID or create the SSM parameters)")
	}
	return vals[s2Name], vals[swidName], nil
}
