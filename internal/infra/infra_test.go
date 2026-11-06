package infra

import (
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/portprobe"
)

func envVar(k, v string) *domain.EnvVar {
	return &domain.EnvVar{Key: k, Value: v, Default: v}
}

func TestInferPostgresAllocatesHostPort(t *testing.T) {
	alloc := portprobe.New()
	alloc.Reserve(PortPostgres) // simulate a local Postgres already using 5432
	infras, err := Infer(nil, map[string]bool{"pg": true}, Options{Allocator: alloc})
	if err != nil {
		t.Fatal(err)
	}
	if len(infras) != 1 {
		t.Fatalf("expected 1 infra, got %d", len(infras))
	}
	pg := infras[0]
	if pg.Port != 5432 {
		t.Fatalf("container port = %d", pg.Port)
	}
	if pg.HostPort == 5432 || pg.HostPort == 0 {
		t.Fatalf("expected remapped host port, got %d", pg.HostPort)
	}
	if pg.Mode != domain.InfraModeLocal {
		t.Fatalf("mode = %q", pg.Mode)
	}
}

func TestEnvBindingsUseContainerPort(t *testing.T) {
	alloc := portprobe.New()
	alloc.Reserve(PortPostgres)
	infras, err := Infer(nil, map[string]bool{"pg": true}, Options{Allocator: alloc})
	if err != nil {
		t.Fatal(err)
	}
	bindings := GenerateEnvBindings(infras)
	want := "postgresql://app:app@postgres:5432/app_db"
	if bindings["DATABASE_URL"] != want {
		t.Fatalf("DATABASE_URL = %q want %q", bindings["DATABASE_URL"], want)
	}
	if strings.Contains(bindings["DATABASE_URL"], itoa(infras[0].HostPort)) {
		t.Fatal("binding must not use the host port")
	}
}

func TestCloudSuppressionPostgres(t *testing.T) {
	env := []*domain.EnvVar{envVar("DATABASE_URL", "postgresql://u:p@db.prod.rds.amazonaws.com:5432/app")}
	infras, err := Infer(env, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(infras) != 1 {
		t.Fatalf("expected 1 infra, got %d", len(infras))
	}
	pg := infras[0]
	if pg.Mode != domain.InfraModeExternal || pg.Provider != "aws-rds" || pg.HostPort != 0 {
		t.Fatalf("expected external aws-rds, got %+v", pg)
	}
}

func TestCloudSuppressionMongoSRV(t *testing.T) {
	env := []*domain.EnvVar{envVar("MONGODB_URI", "mongodb+srv://u:p@cluster0.mongodb.net/app")}
	infras, err := Infer(env, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(infras) != 1 || infras[0].Mode != domain.InfraModeExternal || infras[0].Provider != "mongodb-atlas" {
		t.Fatalf("unexpected %+v", infras)
	}
}

func TestCeleryInfersRedisNotRabbitMQ(t *testing.T) {
	infras, err := Infer(nil, map[string]bool{"celery": true}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(infras) != 1 || infras[0].Type != domain.InfraRedis {
		t.Fatalf("expected only redis, got %+v", infras)
	}
}

func TestExplicitRabbitMQ(t *testing.T) {
	infras, err := Infer(nil, map[string]bool{"pika": true}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range infras {
		if s.Type == domain.InfraRabbitMQ {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected rabbitmq, got %+v", infras)
	}
}

func TestInferDeterministicOrder(t *testing.T) {
	deps := map[string]bool{"pg": true, "redis": true, "kafkajs": true, "mongoose": true}
	a, err := Infer(nil, deps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Infer(nil, deps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatal("length mismatch")
	}
	for i := range a {
		if a[i].Type != b[i].Type {
			t.Fatalf("nondeterministic order at %d: %s vs %s", i, a[i].Type, b[i].Type)
		}
	}
}

func TestExternalBindingsSkipped(t *testing.T) {
	infras := []*domain.InfraService{{Type: domain.InfraPostgres, Name: "postgres", Port: 5432, Mode: domain.InfraModeExternal}}
	bindings := GenerateEnvBindings(infras)
	if len(bindings) != 0 {
		t.Fatalf("external infra must not generate bindings: %+v", bindings)
	}
}

func TestApplyHostPorts(t *testing.T) {
	infras := []*domain.InfraService{{Type: domain.InfraRedis, Name: "redis", Port: 6379, Mode: domain.InfraModeLocal}}
	alloc := portprobe.New()
	if err := ApplyHostPorts(infras, alloc); err != nil {
		t.Fatal(err)
	}
	if infras[0].HostPort == 0 {
		t.Fatal("expected host port allocation")
	}
}
