package manager

import (
	"bus/app/internal/app"
	"bus/app/internal/lib/logging"
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/cockroachdb/errors"
	svc "github.com/kardianos/service"
)

type Srv interface {
	Run(port string, handler http.Handler) error
	Shutdown(ctx context.Context) error
}

type Svc struct {
	app.App
	Srv     Srv
	port    string
	handler http.Handler
}

var errServerClosed = errors.New("http.ListenAndServe: http: Server closed")

func New(srv Srv, handler http.Handler, port string, app app.App) *Svc {
	return &Svc{
		Srv:     srv,
		handler: handler,
		port:    port,
		App:     app,
	}
}

func (p *Svc) Start(s svc.Service) error {

	i := "Starting a web-server on port"
	version := p.Config.Version
	build := p.Config.FixedFileInfo.FileVersion.Build

	p.Logger.Info(i,
		p.Logger.Str("port", p.port),
		p.Logger.Str("version", version),
		p.Logger.Str("build", fmt.Sprintf("%v", build)))

	if p.Config.Log.OutputInFile {
		fmt.Printf("%s: %s version=%s build=%v\n", i, p.port, version, build)
	}

	go p.Run()
	return nil
}

func (p *Svc) Run() {

	logger := logging.GetLogger()

	if err := p.Srv.Run(p.port, p.handler); err != nil && err.Error() != errServerClosed.Error() {
		logger.Error("error running server",
			logger.Err(err))
		os.Exit(1)
	}

}

func (p *Svc) Stop(s svc.Service) error {

	app := p.App
	app.Logger.Debug("Server Shutting down")

	if app.Cancel != nil {
		app.Cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app.Store.Close()

	if err := p.Srv.Shutdown(ctx); err != nil {
		app.Logger.Error("error on server shutting down",
			app.Logger.Err(err))
		return err
	}

	i := "Server is stopped"
	app.Logger.Info(i)
	if p.Config.Log.OutputInFile {
		fmt.Printf("%s\n", i)
	}

	return nil

}
