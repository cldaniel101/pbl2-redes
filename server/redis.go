package main

// NOVO: Importa a biblioteca do Redis
import (
	"context"
	"github.com/go-redis/redis/v8"
	"time"
    "errors"
)

// Erro para quando a trava não puder ser adquirida
var ErrLockNotAcquired = errors.New("lock not acquired")

// Estrutura que gerencia a trava distribuída usando Redis
type RedisLockManager struct {
	client *redis.Client
}

func NewRedisLockManager(redisAddr string) *RedisLockManager {
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr, // Ex: "localhost:6379"
	})
	return &RedisLockManager{client: rdb}
}

// Acquire tenta adquirir a trava. Retorna um valor aleatório (token) em caso de sucesso.
func (rlm *RedisLockManager) Acquire(ctx context.Context, lockKey string, serverID string, ttl time.Duration) (bool, error) {
	// O comando SET com as opções NX e PX é a base da exclusão mútua.
	// Ele só terá sucesso se a chave `lockKey` não existir.
	// A operação é atômica no Redis.
	ok, err := rlm.client.SetNX(ctx, lockKey, serverID, ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// Release libera a trava. Usa um script Lua para garantir a atomicidade.
func (rlm *RedisLockManager) Release(ctx context.Context, lockKey string, serverID string) error {
	// É crucial verificar se o valor da chave ainda é o mesmo que o nosso serverID.
	// Isso evita que um servidor libere uma trava que expirou e foi readquirida por outro.
	// Um script Lua garante que a verificação e a exclusão sejam atômicas.
	script := `
        if redis.call("get", KEYS[1]) == ARGV[1] then
            return redis.call("del", KEYS[1])
        else
            return 0
        end
    `
	_, err := rlm.client.Eval(ctx, script, []string{lockKey}, serverID).Result()
	return err
}