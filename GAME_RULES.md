# Attribute War — Game Spec (v1)

## 1) Objetivo do jogo

Derrubar o oponente em duelo **1v1** reduzindo seus **Pontos de Vida (HP)** a **0**.

## 2) Entidades e conceitos

### 2.1 Jogador

* **HP inicial**: `20`
* **Mão inicial**: `5` cartas
* **Deck**: gerenciado pelo servidor. Após jogar uma carta, compra automaticamente outra para manter a mão com 5 cartas.
* **Coleção**: conjunto de cartas “possuídas” para fins de economia (padrão: populada por cartas básicas; pode crescer ao abrir pacotes).

### 2.2 Carta

* **Campos**:

  * `id: string`
  * `name: string` (ex.: `"Fire Dragon"`)
  * `element: "FIRE" | "WATER" | "PLANT"`
  * `atk: int` (ex.: `8`)
  * `def: int` (ex.: `5`)
* **Autoridade**: **somente o servidor** considera os valores reais da carta (o cliente nunca envia ATK/DEF, apenas `cardId`).

### 2.3 Elementos (pedra-papel-tesoura)

* **FIRE** vence **PLANT**
* **PLANT** vence **WATER**
* **WATER** vence **FIRE**
* **Bônus elemental**: `+3` no **ATK** da **carta com vantagem** **na rodada**.

### 2.4 Parâmetros (configuráveis)

```text
HP_START=20
HAND_SIZE=5
ELEMENTAL_ATK_BONUS=3
ROUND_PLAY_TIMEOUT_MS=12000       # tempo p/ o jogador escolher carta
MATCH_IDLE_TIMEOUT_MS=60000       # desconexão/inatividade
DECK_POLICY=RESHUFFLE_DISCARD     # ou INFINITE_GENERATOR (mais simples)
```

---

## 3) Fluxo da partida

### 3.1 Matchmaking

1. Cliente conecta (TCP) e envia `FIND_MATCH`.
2. Servidor adiciona o jogador na `matchmakingQueue`.
3. Ao haver dois jogadores, servidor cria **Match**, remove ambos da fila e envia `MATCH_FOUND` (contendo `opponentId` e `matchId`).

### 3.2 Preparação

1. Servidor gera/atribui **deck** e **mão inicial (5)** para cada jogador.
2. Envia `STATE` com snapshot completo (HPs, mão, turno/rodada, relógios).
3. **Ordem de rodada**: **revelação simultânea** (ambos escolhem 1 carta).

   * Nota: a “ordem de início” pode ser usada apenas como **desempate** em casos raros (ver §3.4).

### 3.3 Rodadas (ciclo)

1. **Escolha**: cada jogador envia `PLAY {cardId}` dentro do `ROUND_PLAY_TIMEOUT_MS`.

   * Se não enviar a tempo: servidor **auto-seleciona** uma carta aleatória da mão (fail-safe).
2. **Resolução** (servidor):

   * Calcula bônus elemental de cada carta.
   * Calcula danos:

     * `dmgP1 = max(0, (atk1 + bonus1) - def2)`
     * `dmgP2 = max(0, (atk2 + bonus2) - def1)`
   * Aplica danos simultaneamente aos HPs dos **oponentes**.
   * Descarta as duas cartas jogadas e repõe a mão para `HAND_SIZE`.
3. **Notificação**:

   * Envia `ROUND_RESULT` com detalhes (cartas, bônus, dano causado/recebido, HPs finais).
   * Em seguida, envia `STATE` atualizado.

### 3.4 Término e empates

* **Fim**: quando **HP ≤ 0** de um jogador ao fim da resolução da rodada.
* **Empate**: se **ambos** ≤ 0 **na mesma rodada** → resultado `DRAW`.
* **Desempate alternativo (opcional)**: se desejar evitar empates, aplicar ordem de início da partida como critério (quem iniciou **perde** em empate para favorecer o oponente). Por padrão, manter `DRAW` para simplicidade.

---

## 4) Economia: pacotes de cartas (estoque global)

> Requisito do problema: aquisição de novas cartas por **abertura de pacotes**, com **estoque global** e **justiça sob concorrência**.&#x20;

### 4.1 Conceitos

* **Pack**: item consumível contendo `N` cartas (ex.: `N=3`) tiradas de uma **pool** com raridades.
* **Estoque global**: contador/coleção de pacotes disponíveis mantido **apenas no servidor**.

### 4.2 Operação concorrente

* Cliente solicita `OPEN_PACK`.
* Servidor executa **operação atômica**:

  1. Verifica estoque (`> 0`).
  2. **Reserva** uma unidade (decremento atômico).
  3. Sorteia cartas de acordo com raridades (PRNG com seed opcional p/ reprodutibilidade).
  4. Entrega as cartas ao jogador e confirma via `PACK_OPENED`.
* **Se** dois clientes disputam o **último pack**: apenas o **primeiro commit atômico** ganha; o outro recebe `ERROR {code: "OUT_OF_STOCK"}`.
* **Auditoria**: logar `packId`, `playerId`, `cards[]`, `ts`.

---

## 5) Protocolo de comunicação (TCP, JSONL)

**Transporte:** TCP puro com **JSON Lines** (1 objeto JSON por linha `\n`).
**Sem** frameworks de rede; usar apenas **sockets nativos**.&#x20;

### 5.1 Mensagens — Cliente → Servidor

```json
{ "t": "FIND_MATCH" }
{ "t": "PLAY", "cardId": "c_123" }
{ "t": "CHAT", "text": "gl hf!" }
{ "t": "PING", "ts": 1694272000123 }
{ "t": "OPEN_PACK" }
{ "t": "LEAVE" }
```

### 5.2 Mensagens — Servidor → Cliente

```json
{ "t": "MATCH_FOUND", "matchId": "m_001", "opponentId": "p_b" }
{ "t": "STATE",
  "you": { "hp": 20, "hand": ["c_1","c_2","c_3","c_4","c_5"] },
  "opponent": { "hp": 20, "handSize": 5 },
  "round": 1, "deadlineMs": 12000
}
{ "t": "ROUND_RESULT",
  "you": { "cardId": "c_1", "elementBonus": 3, "dmgDealt": 3, "dmgTaken": 1, "hp": 19 },
  "opponent": { "cardId": "c_7", "elementBonus": 0, "hp": 17 },
  "logs": ["You played Fire Dragon (ATK 8). Opponent played Ice Mage (DEF 5)."]
}
{ "t": "PACK_OPENED", "cards": ["c_21","c_88","c_90"], "stock": 137 }
{ "t": "ERROR", "code": "OUT_OF_STOCK", "msg": "No packs left." }
{ "t": "PONG", "ts": 1694272000123, "rttMs": 42 }
{ "t": "MATCH_END", "result": "WIN" | "LOSE" | "DRAW" }
```

> **Observação**: o servidor **nunca** envia atributos de cartas do oponente além de `handSize` e `cardId` **após** a revelação (antes da revelação, apenas `handSize`).

### 5.3 Códigos de erro (mínimos)

* `INVALID_MESSAGE`, `INVALID_CARD`, `NOT_YOUR_TURN` (se optar por turnos não simultâneos),
* `TIMEOUT_PLAY`, `MATCH_NOT_FOUND`, `OUT_OF_STOCK`, `INTERNAL`.

---

## 6) Lógica de dano (pseudocódigo)

```python
def elemental_bonus(a, b):
    # FIRE>PLANT, PLANT>WATER, WATER>FIRE
    if (a == "FIRE" and b == "PLANT") or \
       (a == "PLANT" and b == "WATER") or \
       (a == "WATER" and b == "FIRE"):
        return ELEMENTAL_ATK_BONUS
    return 0

def resolve_round(p1_card, p2_card):
    b1 = elemental_bonus(p1_card.element, p2_card.element)
    b2 = elemental_bonus(p2_card.element, p1_card.element)
    dmg_to_p2 = max(0, (p1_card.atk + b1) - p2_card.def)
    dmg_to_p1 = max(0, (p2_card.atk + b2) - p1_card.def)
    return dmg_to_p1, dmg_to_p2
```

---

## 7) Máquina de estados (servidor)

`IDLE → QUEUED → MATCHING → IN_MATCH → (END | DISCONNECTED)`

* **IDLE**: conectado, fora de partida.
* **QUEUED**: após `FIND_MATCH`.
* **IN\_MATCH**:

  * Subestados por rodada: `AWAITING_PLAYS → RESOLVING → BROADCASTING → NEXT_ROUND`.
  * Timeouts:

    * **Play timeout**: auto-play.
    * **Idle timeout**: desconecta e concede **vitória** ao oponente.

---

## 8) Persistência mínima (em memória p/ MVP)

* **PlayersOnline**: `playerId → socket, lastPing, status`
* **MatchmakingQueue**: fila FIFO
* **Matches**: `matchId → {players[2], hp[], hands[], discard[], round, timers}`
* **CardDB**: `cardId → {name, element, atk, def}`
* **Packs**: `stock: int`, `rngSeed`, `rarityTable`, `auditLog[]`

> Depois, opcionalmente persistir em arquivo/DB; para a disciplina, manter **em memória** é suficiente.

---

## 9) Conectividade, latência e confiabilidade

* **PING/PONG**: cliente envia `PING {ts}` a cada `5s`; servidor responde com `PONG {ts, rttMs}`. Exibir RTT no cliente (requisito de visualizar atraso).&#x20;
* **Keep-alive**: se não houver tráfego por `30s`, enviar `PING`.
* **Reconexão rápida** (opcional): permitir reconectar ao `matchId` se o socket cair por menos de `10s`.

---

## 10) Testes e concorrência (guia)

* **Estresse de Matchmaking**: simular `N` clientes tentando `FIND_MATCH` em bursts.
* **Concorrência de `OPEN_PACK`**: dezenas de clientes disputando o **mesmo** estoque (esperado: nenhum pack duplicado, nenhum “pack perdido”).
* **Simultaneidade de `PLAY`**: ambos enviam perto do deadline; servidor deve:

  * registrar ordem de chegada,
  * resolver simultaneamente sem favorecer quem chegou primeiro,
  * aplicar auto-play quando necessário.
* **Medições**: coletar CPU, memória, RTT médio, throughput de rounds/segundo.
* **Docker**: subir **múltiplas instâncias de cliente** e **1 servidor** via Compose (restrição da disciplina).&#x20;

---

## 11) Segurança e validações

* Ignorar **campos desconhecidos** e mensagens malformadas.
* Validar que `cardId` **está na mão** do jogador no momento do `PLAY`.
* Rate-limit básico para `CHAT` e `PING`.
* Todos os sorteios/prêmios auditar em log.

---

## 12) Estrutura de pastas (sugestão)

```
/server
  /src
    cards.json
    server.py
    match.py
    packs.py
    protocol.py
  Dockerfile
/client
  /src
    client.py
  Dockerfile
/tests
  stress_match.py
  stress_packs.py
README.md
```

---

## 13) Exemplo de `cards.json` (mínimo)

```json
[
  { "id": "c_001", "name": "Fire Dragon", "element": "FIRE",  "atk": 8, "def": 5 },
  { "id": "c_002", "name": "Ice Mage",    "element": "WATER", "atk": 6, "def": 6 },
  { "id": "c_003", "name": "Vine Beast",  "element": "PLANT", "atk": 7, "def": 4 }
]
```

---

## 14) Resumo do loop do jogo (TL;DR)

1. Conecta → `FIND_MATCH` → `MATCH_FOUND`.
2. Recebe `STATE` (mão 5, HP 20).
3. A cada rodada: envia `PLAY {cardId}` (ou auto-play no timeout).
4. Servidor resolve simultaneamente, aplica bônus elemental `+3`, calcula dano `max(0, ATK+bonus - DEF)`, atualiza HP, repõe carta.
5. `ROUND_RESULT` + `STATE`.
6. Quando algum HP ≤ 0: `MATCH_END` com `WIN/LOSE/DRAW`.
