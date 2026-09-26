// Command api serves the sports API locally (net/http) or on AWS Lambda behind an
// API Gateway HTTP API.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vsnandy/sports-api-go/internal/config"
	"github.com/vsnandy/sports-api-go/internal/httpapi"
	"github.com/vsnandy/sports-api-go/internal/players"
	"github.com/vsnandy/sports-api-go/internal/providers/espn"
	"github.com/vsnandy/sports-api-go/internal/providers/httpx"
	"github.com/vsnandy/sports-api-go/internal/providers/sleeper"
	"github.com/vsnandy/sports-api-go/internal/service"
)

const upstreamTimeout = 5 * time.Second

func main() {
	slog.SetDefault(slog.New(httpapi.NewLogHandler(slog.NewJSONHandler(os.Stdout, nil))))
	ctx := context.Background()

	handler, cfg, err := build(ctx)
	if err != nil {
		slog.Error("startup failed", "err", err)
		os.Exit(1)
	}

	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		lambda.Start(httpadapter.NewV2(handler).ProxyWithContext)
		return
	}
	addr := ":" + cfg.Port
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func build(ctx context.Context) (http.Handler, config.Config, error) {
	var ssmClient config.SSMAPI
	var s3Client *s3.Client
	if config.NeedsAWS(os.Getenv) {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, config.Config{}, err
		}
		ssmClient = ssm.NewFromConfig(awsCfg)
		s3Client = s3.NewFromConfig(awsCfg)
	}

	cfg, err := config.Load(ctx, os.Getenv, ssmClient)
	if err != nil {
		return nil, config.Config{}, err
	}

	sl := sleeper.New(httpx.New("sleeper", upstreamTimeout), sleeper.DefaultAPIBase, sleeper.DefaultStatsBase, cfg.SleeperUsername)
	providers := []service.LeagueProvider{sl}
	if len(cfg.ESPNLeagueIDs) > 0 {
		providers = append(providers, espn.New(httpx.New("espn", upstreamTimeout), espn.DefaultBase, cfg.ESPNS2, cfg.ESPNSWID, cfg.ESPNLeagueIDs))
	}

	var store players.Store = players.NoStore{}
	if cfg.PlayersBucket != "" {
		store = players.S3Store{Client: s3Client, Bucket: cfg.PlayersBucket}
	}
	idx := players.New(sl, store, time.Now)

	svc := service.New(providers, sl, sl, idx, time.Now)
	return httpapi.NewHandler(svc, cfg.APIKey), cfg, nil
}
