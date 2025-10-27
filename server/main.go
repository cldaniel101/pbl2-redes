package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	// Importa os novos pacotes modulares
	"pingpong/server/api"
	"pingpong/server/matchmaking"
	"pingpong/server/network"
	"pingpong/server/pubsub"
	"pingpong/server/state"
	"pingpong/server/token"
)

func main() {
	log.Println("[MAIN] A iniciar o servidor do jogo...")

	// 1. Configuração da Topologia do Cluster a partir de Variáveis de Ambiente
	tcpAddr := getEnv("LISTEN_ADDR", ":9000")
	apiAddr := getEnv("API_ADDR", ":8000")
	thisServerAddress := "http://" + getEnv("HOSTNAME", "localhost") + apiAddr

	allServersEnv := getEnv("ALL_SERVERS", thisServerAddress)
	allServers := strings.Split(allServersEnv, ",")
	myIndex := -1
	for i, addr := range allServers {
		if addr == thisServerAddress {
			myIndex = i
			break
		}
	}
	if myIndex == -1 {
		log.Fatalf("[MAIN] Endereço do servidor %s não encontrado na lista ALL_SERVERS", thisServerAddress)
	}

	nextIndex := (myIndex + 1) % len(allServers)
	nextServerAddress := allServers[nextIndex]
	log.Printf("[MAIN] Topologia do anel configurada. Eu sou %s. O próximo é %s.", thisServerAddress, nextServerAddress)

	// 2. Inicialização dos Componentes Centrais
	stateManager := state.NewStateManager(allServers)
	broker := pubsub.NewBroker()

	// Canal para o APIServer notificar o MatchmakingService quando o token chegar.
	tokenAcquiredChan := make(chan bool, 1)

	// 2.1. Inicialização do Token (apenas no primeiro servidor)
	var initialToken *token.Token
	if myIndex == 0 {
		// O primeiro servidor cria e carrega o token com as cartas
		initialToken = token.NewToken(thisServerAddress)

		// Lê o arquivo cards.json
		cardsData, err := ioutil.ReadFile("cards.json")
		if err != nil {
			log.Fatalf("[MAIN] Erro ao ler cards.json: %v", err)
		}

		// Carrega as cartas no token (100 cópias de cada carta)
		if err := initialToken.LoadCardsFromJSON(cardsData, 100); err != nil {
			log.Fatalf("[MAIN] Erro ao carregar cartas no token: %v", err)
		}

		log.Printf("[MAIN] Token inicial criado com %d cartas", initialToken.GetPoolSize())
	}

	// 3. Injeção de Dependências e Inicialização dos Serviços

	// Serviço de Matchmaking (executado em segundo plano)
	matchmakingService := matchmaking.NewService(
		stateManager,
		broker,
		tokenAcquiredChan,
		thisServerAddress,
		allServers,
		nextServerAddress,
		initialToken,
	)

	// Servidor da API (para comunicação entre servidores)
	apiServer := api.NewServer(
		stateManager,
		broker,
		tokenAcquiredChan,
		thisServerAddress,
		matchmakingService, // TokenReceiver
		matchmakingService, // CardProvider
	)

	// Servidor TCP (para comunicação com os clientes)
	tcpServer := network.NewTCPServer(
		tcpAddr,
		stateManager,
		broker,
	)

	// 4. Iniciar os Serviços em Goroutines

	// Inicia o servidor da API HTTP em segundo plano
	go func() {
		log.Printf("[MAIN] A iniciar servidor da API em %s...", apiAddr)
		if err := http.ListenAndServe(apiAddr, apiServer.Router()); err != nil {
			log.Fatalf("[MAIN] Erro fatal no servidor da API: %v", err)
		}
	}()

	// Inicia o serviço de matchmaking em segundo plano
	go matchmakingService.Run()

	// 5. Lógica de Arranque do Token
	// O primeiro servidor na lista é responsável por criar e iniciar o token.
	if myIndex == 0 {
		log.Println("[MAIN] Eu sou o nó inicial. A criar e a passar o token pela primeira vez após 5 segundos...")
		go func() {
			time.Sleep(5 * time.Second) // Espera um pouco para os outros servidores estarem online
			tokenAcquiredChan <- true
		}()
	}

	// 6. Iniciar o Servidor TCP (bloqueia a goroutine principal)
	// Este deve ser o último passo, pois Listen() é uma operação de bloqueio.
	log.Printf("[MAIN] A iniciar servidor TCP para jogadores em %s...", tcpAddr)
	if err := tcpServer.Listen(); err != nil {
		log.Fatalf("[MAIN] Erro fatal no servidor TCP: %v", err)
	}
}

// getEnv lê uma variável de ambiente ou retorna um valor padrão.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
