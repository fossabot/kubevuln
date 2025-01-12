package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/armosec/armoapi-go/apis"
	"github.com/gin-gonic/gin"
	"github.com/kubescape/go-logger"
	"github.com/kubescape/go-logger/helpers"
	"github.com/kubescape/kubevuln/adapters"
	v1 "github.com/kubescape/kubevuln/adapters/v1"
	"github.com/kubescape/kubevuln/controllers"
	"github.com/kubescape/kubevuln/core/services"
	"github.com/kubescape/kubevuln/repositories"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

func main() {
	// Create context that listens for the interrupt signal from the OS.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// to enable otel, set OTEL_COLLECTOR_SVC=otel-collector:4317
	if otelHost, present := os.LookupEnv("OTEL_COLLECTOR_SVC"); present {
		ctx = logger.InitOtel("kubevuln",
			os.Getenv("RELEASE"),
			os.Getenv("ACCOUNT_ID"),
			url.URL{Host: otelHost})
		defer logger.ShutdownOtel(ctx)
	}

	repository := repositories.NewMemoryStorage() // TODO add real storage
	sbomAdapter := v1.NewSyftAdapter()
	cveAdapter, _ := v1.NewGrypeAdapter(ctx)
	platform := adapters.NewMockPlatform() // TODO add real platform
	service := services.NewScanService(sbomAdapter, repository, cveAdapter, repository, platform)
	controller := controllers.NewHTTPController(service, 1) // TODO set with config file

	router := gin.Default() // TODO set release mode: gin.SetMode(gin.ReleaseMode)
	router.Use(otelgin.Middleware("kubevuln-svc"))

	router.GET("/v1/ready", controller.Ready)
	router.POST(fmt.Sprintf("%s/%s", apis.WebsocketScanCommandVersion, apis.SBOMCalculationCommandPath), controller.GenerateSBOM)
	router.POST(fmt.Sprintf("%s/%s", apis.WebsocketScanCommandVersion, apis.WebsocketScanCommandPath), controller.ScanCVE)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.L().Ctx(ctx).Fatal("router error", helpers.Error(err))
		}
	}()

	// Listen for the interrupt signal.
	<-ctx.Done()

	// Restore default behavior on the interrupt signal and notify user of shutdown.
	stop()
	logger.L().Info("shutting down gracefully")

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.L().Ctx(ctx).Fatal("server forced to shutdown", helpers.Error(err))
	}

	// Purging the controller worker queue
	controller.Shutdown()

	logger.L().Info("kubevuln exiting")
}
