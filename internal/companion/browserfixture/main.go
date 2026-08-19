// Command browserfixture serves the real Companion HTTP adapter for isolated
// Playwright tests. Browser routes supply API data, so this backend must never
// reach a daemon, Pi, Herdr, or a model endpoint.
package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

type isolatedBackend struct{}

func (isolatedBackend) CompanionDashboard(context.Context) (model.Dashboard, error) {
	return model.Dashboard{}, errors.New("browser fixture API route was not intercepted")
}

func (isolatedBackend) CompanionAgent(context.Context, string, []string, string, bool) (app.CompanionAgentState, error) {
	return app.CompanionAgentState{}, errors.New("browser fixture API route was not intercepted")
}

func (isolatedBackend) SendCompanion(context.Context, string, string, string) (model.AgentMessage, error) {
	return model.AgentMessage{}, errors.New("browser fixture API route was not intercepted")
}

func (isolatedBackend) CreateAgentFromSource(context.Context, app.CreateAgentFromSourceRequest, string) (app.CreateAgentFromSourceResult, error) {
	return app.CreateAgentFromSourceResult{}, errors.New("browser fixture API route was not intercepted")
}

func main() {
	stateDir, err := os.MkdirTemp("", "galpon-companion-browser-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(stateDir)
	st, err := store.Open(stateDir)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	server := app.NewCompanionServer(st, isolatedBackend{}, "http://127.0.0.1:4173")
	if err := server.Serve("127.0.0.1:4173"); err != nil {
		log.Fatal(err)
	}
}
