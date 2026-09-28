// Package config loads settings from the environment and secrets from SSM
// Parameter Store (or local env overrides).
package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type Config struct {
	SleeperUsername string
	ESPNLeagueIDs   []string
	APIKey          string
	ESPNS2          string
	ESPNSWID        string
	PlayersBucket   string
	Port            string
}

type SSMAPI interface {
	GetParameters(ctx context.Context, in *ssm.GetParametersInput, opts ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

var awsEnv = []string{"SSM_API_KEY_PARAM", "SSM_ESPN_S2_PARAM", "SSM_ESPN_SWID_PARAM", "PLAYERS_BUCKET"}

// NeedsAWS reports whether the environment references SSM or S3.
func NeedsAWS(getenv func(string) string) bool {
	return slices.ContainsFunc(awsEnv, func(k string) bool { return getenv(k) != "" })
}

type secret struct {
	dst         *string
	overrideEnv string
	paramEnv    string
	required    bool
}

func Load(ctx context.Context, getenv func(string) string, client SSMAPI) (Config, error) {
	cfg := Config{
		SleeperUsername: strings.TrimSpace(getenv("SLEEPER_USERNAME")),
		PlayersBucket:   getenv("PLAYERS_BUCKET"),
		Port:            getenv("PORT"),
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.SleeperUsername == "" {
		return Config{}, errors.New("SLEEPER_USERNAME is required")
	}
	for _, id := range strings.Split(getenv("ESPN_LEAGUE_IDS"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, _, err := domain.ParseLeagueID("espn:" + id); err != nil {
			return Config{}, fmt.Errorf("ESPN_LEAGUE_IDS: invalid league id %q", id)
		}
		cfg.ESPNLeagueIDs = append(cfg.ESPNLeagueIDs, id)
	}

	useESPN := len(cfg.ESPNLeagueIDs) > 0
	secrets := []secret{
		{&cfg.APIKey, "API_KEY", "SSM_API_KEY_PARAM", true},
		{&cfg.ESPNS2, "ESPN_S2", "SSM_ESPN_S2_PARAM", useESPN},
		{&cfg.ESPNSWID, "ESPN_SWID", "SSM_ESPN_SWID_PARAM", useESPN},
	}
	byParam := map[string]*string{}
	for _, s := range secrets {
		if !s.required {
			continue
		}
		if v := getenv(s.overrideEnv); v != "" {
			*s.dst = v
			continue
		}
		name := getenv(s.paramEnv)
		if name == "" {
			return Config{}, fmt.Errorf("%s or %s is required", s.overrideEnv, s.paramEnv)
		}
		byParam[name] = s.dst
	}

	if len(byParam) > 0 {
		if client == nil {
			return Config{}, errors.New("SSM client required to load secrets")
		}
		out, err := client.GetParameters(ctx, &ssm.GetParametersInput{
			Names: slices.Sorted(maps.Keys(byParam)), WithDecryption: aws.Bool(true),
		})
		if err != nil {
			return Config{}, fmt.Errorf("ssm GetParameters: %w", err)
		}
		if len(out.InvalidParameters) > 0 {
			return Config{}, fmt.Errorf("ssm parameters not found: %s", strings.Join(out.InvalidParameters, ", "))
		}
		for _, p := range out.Parameters {
			if dst, ok := byParam[aws.ToString(p.Name)]; ok {
				*dst = aws.ToString(p.Value)
			}
		}
	}

	for _, s := range secrets {
		if s.required && *s.dst == "" {
			return Config{}, fmt.Errorf("%s resolved to an empty value", s.overrideEnv)
		}
	}
	return cfg, nil
}
