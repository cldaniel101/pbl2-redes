# Attribute War - Multiplayer

Este repositório contém o desenvolvimento de um jogo de cartas online multiplayer. O projeto foca em duelos táticos e coleção de cartas, com toda a lógica de jogo, estado dos jogadores e comunicação gerenciada por um servidor centralizado.

A aplicação cliente-servidor é desenvolvida em Go e a comunicação é realizada via sockets TCP. O ambiente é totalmente containerizado com Docker e Docker Compose para facilitar a execução, o teste e a implantação.

## Funcionalidades Principais

- **Servidor Centralizado**: Gerencia a lógica do jogo, o estado dos jogadores e a comunicação entre eles em tempo real.

- **Comunicação via Sockets**: A comunicação entre cliente e servidor é bidirecional e implementada exclusivamente com a biblioteca nativa de sockets TCP, sem o uso de frameworks externos.

- **Partidas 1v1**: O sistema permite que múltiplos jogadores se conectem simultaneamente e os pareia em duelos únicos, garantindo que um jogador não enfrente vários oponentes ao mesmo tempo.

- **Visualização de Atraso**: Sistema implementado de PING/PONG que permite aos jogadores visualizar a latência (RTT - Round-Trip Time) de sua comunicação com o servidor quando solicitado através do comando `/ping`, exibindo valores como `RTT: 3 ms` no console.

- **Sistema de Pacotes de Cartas**: Mecânica completa de abertura de pacotes com estoque global thread-safe. O servidor gerencia atomicamente as requisições concorrentes, garantindo justiça na distribuição e auditoria completa de todas as transações. Cada pacote contém 3 cartas únicas sorteadas aleatoriamente.

- **Chat em Tempo Real**: Sistema de comunicação entre jogadores baseado em salas, permitindo coordenação e interação social durante as partidas.

- **Sistema de Comandos**: Interface completa de comandos no cliente incluindo `/ping` para latência, `/pack` para abertura de pacotes, `/play` para jogadas, `/hand` para visualizar cartas, e `/help` para ajuda.

- **Ambiente Containerizado**: Todos os componentes são desenvolvidos e testados em contêineres Docker, permitindo a fácil execução e escalabilidade para testes.

## Tecnologias Utilizadas

- **Go**: Linguagem de programação para o desenvolvimento do cliente e do servidor.
- **Docker**: Para a criação das imagens dos contêineres do cliente e do servidor.
- **Docker Compose**: Para orquestrar e gerenciar os contêineres da aplicação.

## Como Executar

Certifique-se de ter o Docker e o Docker Compose instalados em sua máquina.

1. **Clone o repositório** (ou garanta que todos os arquivos estejam no mesmo diretório).

2. **Construa as imagens e inicie os contêineres:**
   O comando a seguir irá construir as imagens, iniciar 1 contêiner para o servidor e 2 contêineres para os clientes em modo "detached" (em segundo plano).

   ```bash
   docker compose up --build -d
   ```

## Testando a Aplicação

Para interagir com a aplicação, você pode se conectar a uma sessão interativa com cada um dos clientes. Para isso, você precisará de duas janelas de terminal.

1. **Liste os contêineres em execução** para obter seus nomes:
   ```bash
   docker ps
   ```
   A saída mostrará os nomes dos contêineres, como `pbl2-redes-client-1` e `pbl2-redes-client-2`.

2. **Abra dois terminais.**

3. **No primeiro terminal**, conecte-se ao primeiro cliente:
   ```bash
   docker attach pbl2-redes-client-1
   ```

4. **No segundo terminal**, conecte-se ao segundo cliente:
   ```bash
   docker run -it --rm --env SERVER_ADDR="host.docker.internal:9001" pingpong-client:latest
   ```

5. **Agora você pode**:
   - **Usar comandos**: Digite `/help` para ver todos os comandos disponíveis
   - **Jogar partidas**: Digite qualquer mensagem para entrar na fila de matchmaking e jogar duelos 1v1
   - **Abrir pacotes**: Use `/pack` para abrir pacotes de cartas (estoque limitado e concorrente)
   - **Gerenciar cartas**: Use `/hand` para ver sua mão e `/play <número>` para jogar cartas
   - **Monitorar a latência**: Use `/ping` para ativar/desativar a exibição de RTT
   - **Testar concorrência**: Execute múltiplos clientes simultaneamente para testar o sistema de pacotes

### Exemplo de Saída do Cliente:
```
[CLIENT] Conectado ao servidor 172.20.0.2:9000
Digite mensagens ou comandos (/help para ajuda):

/help
=== AJUDA ===
  /play <idx> - Jogar carta pelo índice (1-5)
  /hand       - Mostrar sua mão atual
  /ping       - Liga/desliga exibição de RTT
  /pack       - Abrir pacote de cartas
  /help       - Mostrar esta ajuda
  /quit       - Sair do jogo

oi
🎮 Partida encontrada! Oponente: 172.20.0.3:45678

=== RODADA 1 ===
💚 Seu HP: 20 | ❤️ HP do Oponente: 20
🃏 Sua mão (5 cartas):
  [1] Fire Dragon - FIRE (ATK: 8 / DEF: 5)
  [2] Ice Mage - WATER (ATK: 6 / DEF: 6)
  ...

/pack
📦 Tentando abrir pacote...
📦 Pacote aberto! Cartas recebidas: [c_003, c_005, c_002]
📊 Estoque restante: 99 pacotes
```

> **Importante**: Para sair da sessão `attach` sem derrubar o contêiner, use a combinação de teclas `Ctrl+P` e em seguida `Ctrl+Q`.

## Jogando em Máquinas Diferentes (em Rede)

Para jogar com um amigo em outra máquina, uma máquina atuará como o servidor e a outra se conectará a ela através do seu endereço de IP na rede.

**IPs de Exemplo:**

  * **Máquina Principal (Servidor):** `172.16.201.14`
  * **Máquina Secundária (Cliente):** `172.16.201.15`

### Passo 1: Iniciar o Servidor na Máquina Principal

Nesta máquina, vamos iniciar o servidor e apenas um cliente local, que ficará esperando por um oponente.

```bash
# No terminal da Máquina Principal (172.16.201.14)
docker compose up --build --scale client=1 -d
```

  * O servidor estará rodando e acessível na porta `9000`.
  * Um cliente local será iniciado e entrará na fila de matchmaking automaticamente.

### Passo 2: Encontrar o IP da Máquina Principal

Você precisará do endereço de IP da máquina do servidor na rede local.

  * **No Linux/macOS:** `hostname -I`
  * **No Windows:** `ipconfig`

Procure por um endereço como `172.16.201.14`.

### Passo 3: Iniciar o Cliente na Máquina Secundária

Nesta máquina, não usaremos o `docker-compose.yml`. Em vez disso, rodaremos um contêiner do cliente manualmente, apontando para o IP do servidor.

1.  **Construa a imagem do cliente (apenas uma vez):**

    ```bash
    # No terminal da Máquina Secundária (172.16.201.15)
    docker build -t pingpong-client:latest -f client/Dockerfile .
    ```

2.  **Inicie o cliente conectado ao servidor:**
    Substitua `<IP_DA_MAQUINA_PRINCIPAL>` pelo endereço de IP que você encontrou no passo 2.

    ```bash
    # No terminal da Máquina Secundária (172.16.201.15)
    docker run -it --rm --env SERVER_ADDR="<IP_DA_MAQUINA_PRINCIPAL>:9000" pingpong-client:latest
    ```

    Assim que este comando for executado, o cliente se conectará ao servidor, encontrará o outro cliente que já estava na fila, e a partida começará\!

### Passo 4: Controlar os Dois Jogadores

Agora você precisa de um terminal para cada jogador.

1.  **Jogador 1 (Máquina Secundária):** O terminal onde você executou `docker run` já está controlando este jogador.
2.  **Jogador 2 (Máquina Principal):** Abra um novo terminal na máquina principal e use `docker attach` para se conectar ao cliente que foi iniciado pelo Docker Compose.
    ```bash
    # Primeiro, encontre o nome do contêiner
    docker ps

    # Depois, conecte-se a ele (o nome pode variar)
    docker attach pbl2-redes-main-client-1
    ```

Agora você pode jogar uma carta em cada terminal e verá a partida progredir.

### Solução de Problemas de Conexão

Se o cliente na máquina secundária não conseguir se conectar, o problema é quase sempre um **firewall** na máquina principal bloqueando a porta `9000`. Certifique-se de que o firewall do seu sistema operacional (Windows Defender, UFW no Linux, etc.) permite conexões de entrada na porta TCP `9000`.


## Comandos Úteis

- **Visualizar os logs de todos os serviços em tempo real:**
  ```bash
  docker compose logs -f
  ```

- **Visualizar apenas os logs do servidor (monitorar PING/PONG e conexões):**
  ```bash
  docker attach pbl_server
  ```

- **Derrubar o ambiente** (para e remove os contêineres):
  ```bash
  docker compose down -v
  ```

## Testes e Validação

O projeto inclui uma suíte completa de testes para validar a funcionalidade e robustez do sistema:

### Testes de Stress - Sistema de Pacotes

Para testar a concorrência e justiça do sistema de pacotes:

```bash
cd tests
go run stress_packs.go
```

**Resultado esperado:**
- 20 clientes simultâneos disputando 10 pacotes
- Exatamente 10 sucessos e 10 falhas (`OUT_OF_STOCK`)
- Estoque final = 0
- Nenhuma carta duplicada no mesmo pacote
- Log de auditoria completo

### Testes Unitários

Execute os testes automatizados com:

```bash
cd tests
go test -v
```

**Testes incluídos:**
- `TestPackStoreConcurrency`: Validação de concorrência thread-safe
- `TestPackStoreBasicFunctionality`: Testes de funcionalidade básica
- `BenchmarkPackStoreConcurrency`: Benchmark de performance

### Exemplo de Resultado dos Testes:
```
=== TESTE DE STRESS ===
✅ Sucessos: 10
❌ Falhas: 10
📦 Estoque final: 0
🎉 TESTE PASSOU: Todos os critérios foram atendidos!

=== TESTES UNITÁRIOS ===
--- PASS: TestPackStoreConcurrency (0.00s)
--- PASS: TestPackStoreBasicFunctionality (0.00s)
```

## Variáveis de Ambiente

É possível customizar a execução através de variáveis de ambiente no arquivo `docker-compose.yml`.

- `SERVER_ADDR` (cliente): Endereço do servidor ao qual o cliente deve se conectar. Ex: `server:9000`.
- `PING_INTERVAL_MS` (cliente): Intervalo em milissegundos para o envio de PINGs para medição de latência. Padrão: `2000` (2 segundos).
- `LISTEN_ADDR` (servidor): Endereço e porta em que o servidor escutará por conexões. Ex: `:9000`.

## Arquitetura da Aplicação

### Estrutura do Projeto:
```
├── server/
│   ├── main.go              # Servidor principal com handlers
│   ├── cards.json           # Base de dados de cartas
│   ├── packs/
│   │   └── packs.go         # Sistema de pacotes thread-safe
│   ├── game/
│   │   ├── cards.go         # Lógica de cartas e sistema de pacotes
│   │   ├── match.go         # Lógica de partidas e duelos
│   │   └── types.go         # Tipos e constantes do jogo
│   └── protocol/
│       └── protocol.go      # Protocolo de comunicação JSONL
├── client/
│   └── main.go              # Cliente com interface de comandos
├── tests/
│   ├── stress_packs.go      # Teste de stress do sistema de pacotes
│   └── packs_test.go        # Testes unitários e benchmarks
└── docker-compose.yml       # Orquestração dos contêineres
```

### Fluxo de Comunicação:

1. **Inicialização**: Cliente conecta ao servidor via TCP e entra na fila de matchmaking
2. **Matchmaking**: Servidor pareia jogadores em duelos 1v1 automáticos
3. **Duelos**: Sistema de turnos simultâneos com cartas, elementos e cálculo de dano
4. **Pacotes**: Sistema de abertura de pacotes com estoque global e controle de concorrência
5. **Monitoramento**: Sistema PING/PONG para medição de latência em tempo real

### Protocolo de Mensagens (JSONL):

**Cliente → Servidor:**
- `{"t": "FIND_MATCH"}`: Entra na fila de matchmaking
- `{"t": "PLAY", "cardId": "c_001"}`: Joga uma carta específica
- `{"t": "OPEN_PACK"}`: Solicita abertura de pacote
- `{"t": "PING", "ts": 1234567890}`: Ping para medição de latência
- `{"t": "CHAT", "text": "mensagem"}`: Mensagem de chat
- `{"t": "LEAVE"}`: Sair da partida/desconectar

**Servidor → Cliente:**
- `{"t": "MATCH_FOUND", "matchId": "m_001", "opponentId": "p_b"}`: Partida encontrada
- `{"t": "STATE", "you": {...}, "opponent": {...}, "round": 1}`: Estado da partida
- `{"t": "ROUND_RESULT", "you": {...}, "opponent": {...}}`: Resultado da rodada
- `{"t": "PACK_OPENED", "cards": ["c_1", "c_2"], "stock": 99}`: Pacote aberto
- `{"t": "ERROR", "code": "OUT_OF_STOCK", "msg": "..."}`: Mensagem de erro
- `{"t": "PONG", "ts": 1234567890, "rttMs": 42}`: Resposta de ping

### Comandos do Cliente:

- `/help`: Mostra a lista completa de comandos disponíveis
- `/play <índice>`: Joga uma carta pelo índice (1-5) durante uma partida
- `/hand`: Exibe as cartas na mão atual do jogador
- `/pack`: Abre um pacote de cartas (consome do estoque global)
- `/ping`: Liga/desliga a exibição de RTT (latência) no console
- `/quit`: Sai do jogo e desconecta do servidor