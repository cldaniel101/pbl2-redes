# Testando o Token com Stack Global de Cartas

## Verificações para Confirmar a Implementação

### 1. Logs do Token

Ao iniciar o cluster, você deve ver logs como:

```
[MAIN] Token inicial criado com 900 cartas
[MATCHMAKING] Token recebido com 900 cartas no pool. A verificar a fila...
[MATCHMAKING] Pegou 10 cartas do token para a partida
[MATCHMAKING] A passar o token (890 cartas) para http://server-2:8000...
```

### 2. Criar Partidas e Observar

1. Conecte 2 clientes ao Servidor 1
2. Observe o log mostrando cartas sendo retiradas do token
3. Conecte mais clientes a outros servidores
4. Observe o token circulando com menos cartas

### 3. Verificar Partidas Distribuídas

1. Conecte 1 cliente ao Servidor 1
2. Conecte 1 cliente ao Servidor 2
3. Observe os logs mostrando:
   - Servidor 1 retira 10 cartas do token
   - Servidor 1 envia 5 cartas para Servidor 2 na requisição HTTP
   - Ambos criam a partida com as cartas corretas

### 4. Testar Reabastecimento

Para forçar um reabastecimento (se necessário para testes):

1. Modifique temporariamente `LoadCardsFromJSON` para usar poucas cópias (ex: 1)
2. Crie múltiplas partidas rapidamente
3. Observe o log: `[TOKEN] Pool insuficiente (X cartas), reabastecendo...`
4. Confirme: `[TOKEN] Pool reabastecido. Total de cartas agora: Y`

## Comandos Úteis para Debug

### Ver logs do token em tempo real

```bash
docker-compose logs -f server-1 | grep TOKEN
```

### Ver logs de matchmaking

```bash
docker-compose logs -f server-1 | grep MATCHMAKING
```

### Ver todas as cartas retiradas

```bash
docker-compose logs | grep "Pegou.*cartas do token"
```

## Possíveis Problemas e Soluções

### Problema: "token não disponível"

**Causa**: Servidor tentou criar partida sem ter o token.

**Solução**: 
- Isso é esperado se o servidor não estiver com o token no momento
- O token circula entre servidores, então às vezes não estará disponível
- Os jogadores permanecerão na fila até o token chegar

### Problema: "erro ao pegar cartas do token: pool insuficiente"

**Causa**: Pool esgotou e o reabastecimento também falhou.

**Solução**:
- Verifique se `cards.json` está sendo carregado corretamente
- Verifique se há cartas suficientes no arquivo
- Confirme que o método `refillPool_unsafe()` está sendo chamado

### Problema: Cartas duplicadas entre jogadores

**Causa**: Isso NÃO deve acontecer com esta implementação.

**Solução**: 
- Se ocorrer, há um bug crítico
- Verifique os logs para ver se ambos os servidores estão pegando cartas do mesmo token
- Confirme que `DrawCards()` está removendo as cartas do pool

## Validações Importantes

✅ **O token deve circular continuamente** entre os servidores

✅ **O número de cartas no token deve diminuir** conforme partidas são criadas

✅ **O pool deve se reabastecer automaticamente** quando baixo

✅ **Partidas distribuídas devem receber cartas diferentes** para cada jogador

✅ **Não deve haver duplicação de cartas** entre partidas simultâneas

## Métricas para Monitorar

1. **Taxa de criação de partidas**: Quantas partidas/minuto
2. **Velocidade de circulação do token**: Tempo médio por ciclo completo
3. **Frequência de reabastecimento**: Quantas vezes o pool foi reabastecido
4. **Tamanho do pool**: Mínimo e máximo observados

## Teste de Carga

Para testar sob carga:

```bash
# Execute múltiplos clientes simultaneamente
for i in {1..20}; do
    docker run --network pbl2-network client-app &
done
```

Observe:
- O token deve continuar circulando
- Partidas devem ser criadas corretamente
- Pool deve se reabastecer conforme necessário
- Não deve haver deadlocks ou travamentos

