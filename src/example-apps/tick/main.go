package main

import (
	"encoding/json"
	"example-apps/tick/a8"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"code.cloudfoundry.org/localip"

	"github.com/ryanmoran/viron"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
)

type Environment struct {
	VCAPApplication struct {
		ApplicationName string `json:"application_name"`
		InstanceIndex   int    `json:"instance_index"`
	} `env:"VCAP_APPLICATION" env-required:"true"`

	Port            string `env:"PORT"               env-required:"true"`
	RegistryBaseURL string `env:"REGISTRY_BASE_URL"  env-required:"true"`
	StartPort       string `env:"START_PORT"         env-required:"false"`
	ListenPorts     string `env:"LISTEN_PORTS"       env-required:"false"`
}

func main() {
	if err := mainWithError(); err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}
}

func mainWithError() error {
	var env Environment
	err := viron.Parse(&env)
	if err != nil {
		return fmt.Errorf("unable to parse environment: %s", err)
	}

	var startPort int
	if env.StartPort != "" {
		startPort, err = strconv.Atoi(env.StartPort)
		if err != nil {
			return fmt.Errorf("invalid env var START_PORT: %s", err)
		}
	}

	var listenPorts int
	if env.ListenPorts != "" {
		listenPorts, err = strconv.Atoi(env.ListenPorts)
		if err != nil {
			return fmt.Errorf("invalid env var LISTEN_PORTS: %s", err)
		}
	}

	localIP, err := localip.LocalIP()
	if err != nil {
		return fmt.Errorf("unable to discover local ip: %s", err)
	}

	const TTLSeconds = 10
	const PollSeconds = 8

	a8Client := &a8.Client{
		BaseURL:            env.RegistryBaseURL,
		HttpClient:         http.DefaultClient,
		LocalServerAddress: fmt.Sprintf("%s:%s", localIP, env.Port),
		ServiceName:        env.VCAPApplication.ApplicationName,
		TTLSeconds:         TTLSeconds,
	}

	poller := &Poller{
		PollInterval: (PollSeconds * time.Second),
		Action:       a8Client.Register,
	}

	infoHandler := &InfoHandler{
		InfoData: env.VCAPApplication,
	}

	servers := []ifrit.Runner{http_server.New(fmt.Sprintf("0.0.0.0:%s", env.Port), infoHandler)}
	for i := 0; i < listenPorts; i++ {
		servers = append(servers, http_server.New(fmt.Sprintf("0.0.0.0:%d", startPort+i), infoHandler))
	}

	members := grouper.Members{
		{"registration_poller", poller},
	}
	for i, server := range servers {
		members = append(members, grouper.Member{fmt.Sprintf("http_server_%d", i), server})
	}

	monitor := ifrit.Invoke(sigmon.New(grouper.NewOrdered(os.Interrupt, members)))

	err = <-monitor.Wait()
	if err != nil {
		return fmt.Errorf("ifrit monitor: %s", err)
	}

	return nil
}

type InfoHandler struct {
	InfoData interface{}
}

func (h *InfoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(h.InfoData)
}

type Poller struct {
	PollInterval time.Duration
	Action       func() error
}

func (m *Poller) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	err := m.Action()
	if err != nil {
		return err
	}

	close(ready)

	for {
		select {
		case <-signals:
			return nil
		case <-time.After(m.PollInterval):
			err = m.Action()
			if err != nil {
				log.Printf("%s", err)
				continue
			}
		}
	}
}
