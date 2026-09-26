package config

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	values  map[string]string
	gotReq  *ssm.GetParametersInput
	invalid []string
}

func (f *fakeSSM) GetParameters(_ context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	f.gotReq = in
	out := &ssm.GetParametersOutput{InvalidParameters: f.invalid}
	for _, n := range in.Names {
		if v, ok := f.values[n]; ok {
			out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Value: aws.String(v)})
		}
	}
	return out, nil
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadFromSSM(t *testing.T) {
	f := &fakeSSM{values: map[string]string{"/k": "apikey", "/s2": "S2", "/swid": "{SWID}"}}
	cfg, err := Load(context.Background(), env(map[string]string{
		"SLEEPER_USERNAME": "varun", "ESPN_LEAGUE_IDS": " 123 , 456,",
		"SSM_API_KEY_PARAM": "/k", "SSM_ESPN_S2_PARAM": "/s2", "SSM_ESPN_SWID_PARAM": "/swid",
		"PLAYERS_BUCKET": "bucket",
	}), f)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{SleeperUsername: "varun", ESPNLeagueIDs: []string{"123", "456"}, APIKey: "apikey", ESPNS2: "S2", ESPNSWID: "{SWID}", PlayersBucket: "bucket", Port: "8080"}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !aws.ToBool(f.gotReq.WithDecryption) {
		t.Fatal("secrets must be fetched WithDecryption")
	}
}

func TestOverridesSkipSSM(t *testing.T) {
	cfg, err := Load(context.Background(), env(map[string]string{"SLEEPER_USERNAME": "varun", "API_KEY": "dev", "PORT": "9000"}), nil)
	if err != nil || cfg.APIKey != "dev" || cfg.Port != "9000" || len(cfg.ESPNLeagueIDs) != 0 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestESPNSecretsOnlyWhenLeaguesConfigured(t *testing.T) {
	f := &fakeSSM{values: map[string]string{"/k": "apikey"}}
	_, err := Load(context.Background(), env(map[string]string{
		"SLEEPER_USERNAME": "varun", "SSM_API_KEY_PARAM": "/k",
		"SSM_ESPN_S2_PARAM": "/s2", "SSM_ESPN_SWID_PARAM": "/swid",
	}), f)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.gotReq.Names, []string{"/k"}) {
		t.Fatalf("fetched %v, want only the API key", f.gotReq.Names)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]struct {
		env     map[string]string
		ssm     SSMAPI
		wantSub string
	}{
		"missing username":    {map[string]string{"API_KEY": "k"}, nil, "SLEEPER_USERNAME"},
		"missing api key":     {map[string]string{"SLEEPER_USERNAME": "v"}, nil, "API_KEY"},
		"bad league id":       {map[string]string{"SLEEPER_USERNAME": "v", "API_KEY": "k", "ESPN_LEAGUE_IDS": "12/3"}, nil, "ESPN_LEAGUE_IDS"},
		"espn creds missing":  {map[string]string{"SLEEPER_USERNAME": "v", "API_KEY": "k", "ESPN_LEAGUE_IDS": "123"}, nil, "ESPN_S2"},
		"ssm client missing":  {map[string]string{"SLEEPER_USERNAME": "v", "SSM_API_KEY_PARAM": "/k"}, nil, "SSM client"},
		"ssm param not found": {map[string]string{"SLEEPER_USERNAME": "v", "SSM_API_KEY_PARAM": "/k"}, &fakeSSM{invalid: []string{"/k"}}, "/k"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), env(tt.env), tt.ssm)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want mention of %q", err, tt.wantSub)
			}
		})
	}
}

func TestNeedsAWS(t *testing.T) {
	if NeedsAWS(env(map[string]string{"API_KEY": "k"})) {
		t.Fatal("pure local config should not need AWS")
	}
	if !NeedsAWS(env(map[string]string{"PLAYERS_BUCKET": "b"})) {
		t.Fatal("bucket needs AWS")
	}
}
