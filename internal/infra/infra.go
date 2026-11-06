// Package infra infers backing services (databases, caches, brokers, object
// stores) from dependencies and environment variables, suppresses locally
// provisioned services when a managed cloud provider is detected, and allocates
// conflict-free host ports for every locally provisioned container while keeping
// in-network URLs on the standard container port.
package infra

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/portprobe"
)

// Standard container ports.
const (
	PortPostgres      = 5432
	PortRedis         = 6379
	PortMySQL         = 3306
	PortMongo         = 27017
	PortKafka         = 9092
	PortMinIO         = 9000
	PortRabbitMQ      = 5672
	PortElasticsearch = 9200
)

// Options tune inference.
type Options struct {
	SourcePath string
	Allocator  *portprobe.Allocator
}

// Infer returns the inferred infrastructure services in a deterministic order.
func Infer(envVars []*domain.EnvVar, deps map[string]bool, opts Options) ([]*domain.InfraService, error) {
	sig := newSignals(envVars, deps)
	var out []*domain.InfraService

	add := func(kind domain.InfraType, factory func() *domain.InfraService) error {
		wants := sig.wants(kind)
		provider, reason, external := sig.externalFor(kind)
		if external && wants {
			svc := factory()
			svc.Mode = domain.InfraModeExternal
			svc.Provider = provider
			svc.Reason = reason
			svc.HostPort = 0
			svc.Volumes = nil
			svc.Healthcheck = domain.HealthcheckSpec{}
			out = append(out, svc)
			return nil
		}
		if !wants {
			return nil
		}
		svc := factory()
		svc.Mode = domain.InfraModeLocal
		if opts.Allocator != nil {
			port, err := opts.Allocator.Allocate(svc.Port)
			if err != nil {
				return err
			}
			svc.HostPort = port
		} else {
			svc.HostPort = svc.Port
		}
		out = append(out, svc)
		return nil
	}

	for _, step := range []struct {
		kind    domain.InfraType
		factory func() *domain.InfraService
	}{
		{domain.InfraPostgres, PostgresService},
		{domain.InfraMySQL, MySQLService},
		{domain.InfraRedis, RedisService},
		{domain.InfraMongo, MongoService},
		{domain.InfraKafka, KafkaService},
		{domain.InfraRabbitMQ, RabbitMQService},
		{domain.InfraMinIO, MinIOService},
		{domain.InfraElasticsearch, ElasticsearchService},
	} {
		if err := add(step.kind, step.factory); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GenerateEnvBindings returns in-network connection strings for local services.
// External services are skipped so the user's cloud credentials are preserved.
func GenerateEnvBindings(infras []*domain.InfraService) map[string]string {
	out := make(map[string]string)
	for _, svc := range infras {
		if svc.Mode == domain.InfraModeExternal {
			continue
		}
		host := svc.Name
		p := svc.Port
		switch svc.Type {
		case domain.InfraPostgres:
			url := fmt.Sprintf("postgresql://app:app@%s:%d/app_db", host, p)
			out["DATABASE_URL"] = url
			out["POSTGRES_URL"] = url
			out["POSTGRES_HOST"] = host
			out["POSTGRES_PORT"] = itoa(p)
			out["POSTGRES_USER"] = "app"
			out["POSTGRES_PASSWORD"] = "app"
			out["POSTGRES_DB"] = "app_db"
		case domain.InfraMySQL:
			url := fmt.Sprintf("mysql://app:app@%s:%d/app_db", host, p)
			out["MYSQL_URL"] = url
			out["MYSQL_HOST"] = host
			out["MYSQL_PORT"] = itoa(p)
		case domain.InfraRedis:
			url := fmt.Sprintf("redis://%s:%d", host, p)
			out["REDIS_URL"] = url
			out["REDIS_HOST"] = host
			out["REDIS_PORT"] = itoa(p)
		case domain.InfraMongo:
			url := fmt.Sprintf("mongodb://app:app@%s:%d/app_db?authSource=admin", host, p)
			out["MONGO_URI"] = url
			out["MONGODB_URI"] = url
			out["MONGODB_URL"] = url
			out["MONGO_URL"] = url
			out["MONGODB_DB_NAME"] = "app_db"
			out["MONGO_DB_NAME"] = "app_db"
		case domain.InfraKafka:
			brokers := fmt.Sprintf("%s:%d", host, p)
			out["KAFKA_BROKERS"] = brokers
			out["KAFKA_BOOTSTRAP_SERVERS"] = brokers
		case domain.InfraRabbitMQ:
			url := fmt.Sprintf("amqp://app:app@%s:%d", host, p)
			out["RABBITMQ_URL"] = url
			out["CELERY_BROKER_URL"] = url
		case domain.InfraMinIO:
			out["S3_ENDPOINT"] = fmt.Sprintf("http://%s:%d", host, p)
			out["AWS_ENDPOINT_URL"] = fmt.Sprintf("http://%s:%d", host, p)
			out["AWS_ACCESS_KEY_ID"] = "minioadmin"
			out["AWS_SECRET_ACCESS_KEY"] = "minioadmin"
		case domain.InfraElasticsearch:
			out["ELASTICSEARCH_URL"] = fmt.Sprintf("http://%s:%d", host, p)
			out["ELASTIC_URL"] = fmt.Sprintf("http://%s:%d", host, p)
		}
	}
	return out
}

// ApplyHostPorts allocates host ports for any service that lacks one.
func ApplyHostPorts(infras []*domain.InfraService, alloc *portprobe.Allocator) error {
	if alloc == nil {
		return nil
	}
	for _, svc := range infras {
		if svc.Mode == domain.InfraModeExternal {
			continue
		}
		if svc.HostPort == 0 {
			port, err := alloc.Allocate(svc.Port)
			if err != nil {
				return err
			}
			svc.HostPort = port
		} else {
			alloc.Reserve(svc.HostPort)
		}
	}
	return nil
}

// --- Signals ---------------------------------------------------------------

type signals struct {
	deps map[string]bool
	keys map[string]string // original key -> value
}

func newSignals(envVars []*domain.EnvVar, deps map[string]bool) signals {
	keys := make(map[string]string)
	for _, ev := range envVars {
		keys[ev.Key] = firstNonEmpty(ev.Value, ev.Default)
	}
	if deps == nil {
		deps = map[string]bool{}
	}
	return signals{deps: deps, keys: keys}
}

func (s signals) hasDep(names ...string) bool {
	for _, n := range names {
		if s.deps[strings.ToLower(n)] {
			return true
		}
	}
	return false
}

func (s signals) keyHas(substrs ...string) (key, value string, ok bool) {
	keys := make([]string, 0, len(s.keys))
	for k := range s.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		up := strings.ToUpper(k)
		for _, sub := range substrs {
			if strings.Contains(up, strings.ToUpper(sub)) {
				return k, s.keys[k], true
			}
		}
	}
	return "", "", false
}

func (s signals) valueStarts(prefixes ...string) bool {
	for _, v := range s.keys {
		lv := strings.ToLower(strings.TrimSpace(v))
		for _, p := range prefixes {
			if strings.HasPrefix(lv, p) {
				return true
			}
		}
	}
	return false
}

func (s signals) valueContains(substrs ...string) bool {
	for _, v := range s.keys {
		lv := strings.ToLower(v)
		for _, sub := range substrs {
			if strings.Contains(lv, sub) {
				return true
			}
		}
	}
	return false
}

func (s signals) wants(kind domain.InfraType) bool {
	switch kind {
	case domain.InfraPostgres:
		_, _, keyOK := s.keyHas("POSTGRES", "PGDATABASE")
		return s.hasDep("pg", "postgres", "postgresql", "psycopg2", "psycopg2-binary", "asyncpg",
			"github.com/jackc/pgx/v5", "gorm.io/driver/postgres") ||
			keyOK || s.valueStarts("postgres://", "postgresql://", "postgresql+")
	case domain.InfraMySQL:
		_, _, keyOK := s.keyHas("MYSQL")
		return s.hasDep("mysql", "mysql2", "pymysql", "mysqlclient", "gorm.io/driver/mysql") ||
			keyOK || s.valueStarts("mysql://")
	case domain.InfraRedis:
		_, _, keyOK := s.keyHas("REDIS", "CACHE_URL")
		return s.hasDep("redis", "ioredis", "aioredis", "redis-py", "celery", "github.com/redis/go-redis/v9") ||
			keyOK || s.valueStarts("redis://", "rediss://")
	case domain.InfraMongo:
		_, _, keyOK := s.keyHas("MONGO", "MONGODB")
		return s.hasDep("mongoose", "mongodb", "pymongo", "motor", "go.mongodb.org/mongo-driver") ||
			keyOK || s.valueStarts("mongodb://", "mongodb+srv://")
	case domain.InfraKafka:
		_, _, keyOK := s.keyHas("KAFKA")
		return s.hasDep("kafkajs", "confluent-kafka", "kafka-python", "segmentio/kafka-go",
			"github.com/segmentio/kafka-go", "github.com/confluentinc/confluent-kafka-go") || keyOK
	case domain.InfraRabbitMQ:
		_, _, keyOK := s.keyHas("RABBITMQ", "AMQP")
		return s.hasDep("amqplib", "pika", "github.com/rabbitmq/amqp091-go") ||
			keyOK || s.valueStarts("amqp://", "amqps://")
	case domain.InfraMinIO:
		if s.hasDep("minio", "boto3", "aws-sdk", "@aws-sdk/client-s3", "gocloud.dev/blob/s3blob") {
			return true
		}
		_, _, keyOK := s.keyHas("MINIO", "S3_ENDPOINT", "AWS_ENDPOINT_URL", "S3_BUCKET")
		return keyOK
	case domain.InfraElasticsearch:
		_, _, keyOK := s.keyHas("ELASTIC")
		return s.hasDep("@elastic/elasticsearch", "elasticsearch", "github.com/elastic/go-elasticsearch/v8") || keyOK
	}
	return false
}

// externalFor reports a managed provider when the project clearly points at one.
func (s signals) externalFor(kind domain.InfraType) (provider, reason string, external bool) {
	switch kind {
	case domain.InfraPostgres:
		if s.hasDep("@neondatabase/serverless") || s.valueContains("neon.tech") {
			return "neon", "detected Neon serverless Postgres", true
		}
		if s.hasDep("@vercel/postgres") || s.valueContains("vercel-storage.com") {
			return "vercel-postgres", "detected Vercel Postgres", true
		}
		if s.valueContains("supabase.co") {
			return "supabase", "detected Supabase Postgres", true
		}
		if s.valueContains("rds.amazonaws.com") {
			return "aws-rds", "detected AWS RDS Postgres", true
		}
		if s.valueContains("render.com") || s.valueContains("railway.app") || s.valueContains("cockroachlabs.cloud") || s.valueContains("planetscale.com") {
			return "managed-postgres", "detected a managed Postgres endpoint", true
		}
	case domain.InfraMySQL:
		if s.valueContains("planetscale.com") {
			return "planetscale", "detected PlanetScale MySQL", true
		}
		if s.valueContains("rds.amazonaws.com") || s.valueContains("azure.com") {
			return "managed-mysql", "detected a managed MySQL endpoint", true
		}
	case domain.InfraRedis:
		if s.hasDep("@upstash/redis") || s.valueContains("upstash.io") {
			return "upstash", "detected Upstash Redis", true
		}
		if s.valueContains("redislabs.com") || s.valueContains("redis.cloud") {
			return "managed-redis", "detected a managed Redis endpoint", true
		}
	case domain.InfraMongo:
		if s.valueContains("mongodb+srv://") || s.valueContains("mongodb.net") {
			return "mongodb-atlas", "detected MongoDB Atlas (mongodb+srv)", true
		}
	case domain.InfraKafka:
		if s.valueContains("confluent.cloud") || s.valueContains(".cloud:") {
			return "confluent-cloud", "detected Confluent Cloud Kafka", true
		}
	case domain.InfraMinIO:
		if s.valueContains("amazonaws.com") || s.valueContains("cloudflarestorage.com") ||
			s.valueContains("digitaloceanspaces.com") || s.valueContains("storage.googleapis.com") {
			return "object-storage", "detected a managed S3-compatible endpoint", true
		}
		if s.hasDep("boto3", "aws-sdk", "@aws-sdk/client-s3") {
			if _, _, ok := s.keyHas("MINIO", "S3_ENDPOINT", "AWS_ENDPOINT_URL", "S3_BUCKET"); !ok {
				return "aws-s3", "detected AWS SDK without a local endpoint", true
			}
		}
	case domain.InfraRabbitMQ:
		if s.valueContains("cloudamqp.com") {
			return "cloudamqp", "detected CloudAMQP", true
		}
	case domain.InfraElasticsearch:
		if s.valueContains("elastic-cloud.com") || s.valueContains("bonsai.io") {
			return "elastic-cloud", "detected Elastic Cloud", true
		}
	}
	return "", "", false
}

// --- Factories -------------------------------------------------------------

// PostgresService returns a pgvector-enabled Postgres 16.
func PostgresService() *domain.InfraService {
	return &domain.InfraService{
		Type:  domain.InfraPostgres,
		Name:  "postgres",
		Image: "pgvector/pgvector:pg16",
		Port:  PortPostgres,
		Env: map[string]string{
			"POSTGRES_USER":     "app",
			"POSTGRES_PASSWORD": "app",
			"POSTGRES_DB":       "app_db",
		},
		Volumes: []string{"postgres_data:/var/lib/postgresql/data"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"},
			Interval:    "5s",
			Timeout:     "3s",
			Retries:     10,
			StartPeriod: "5s",
		},
	}
}

// RedisService returns Redis 7.
func RedisService() *domain.InfraService {
	return &domain.InfraService{
		Type:    domain.InfraRedis,
		Name:    "redis",
		Image:   "redis:7-alpine",
		Port:    PortRedis,
		Volumes: []string{"redis_data:/data"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD", "redis-cli", "ping"},
			Interval:    "5s",
			Timeout:     "3s",
			Retries:     10,
			StartPeriod: "2s",
		},
	}
}

// MySQLService returns MySQL 8.
func MySQLService() *domain.InfraService {
	return &domain.InfraService{
		Type:  domain.InfraMySQL,
		Name:  "mysql",
		Image: "mysql:8.0",
		Port:  PortMySQL,
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "rootpw",
			"MYSQL_USER":          "app",
			"MYSQL_PASSWORD":      "app",
			"MYSQL_DATABASE":      "app_db",
		},
		Volumes: []string{"mysql_data:/var/lib/mysql"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "mysqladmin ping -h localhost -u app -papp"},
			Interval:    "5s",
			Timeout:     "3s",
			Retries:     10,
			StartPeriod: "10s",
		},
	}
}

// MongoService returns MongoDB 7.
func MongoService() *domain.InfraService {
	return &domain.InfraService{
		Type:  domain.InfraMongo,
		Name:  "mongo",
		Image: "mongo:7",
		Port:  PortMongo,
		Env: map[string]string{
			"MONGO_INITDB_ROOT_USERNAME": "app",
			"MONGO_INITDB_ROOT_PASSWORD": "app",
			"MONGO_INITDB_DATABASE":      "app_db",
		},
		Volumes: []string{"mongo_data:/data/db"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD", "mongosh", "--quiet", "--eval", "db.adminCommand('ping').ok"},
			Interval:    "5s",
			Timeout:     "3s",
			Retries:     10,
			StartPeriod: "5s",
		},
	}
}

// KafkaService returns a single-node KRaft Kafka (no ZooKeeper).
func KafkaService() *domain.InfraService {
	return &domain.InfraService{
		Type:  domain.InfraKafka,
		Name:  "kafka",
		Image: "confluentinc/cp-kafka:7.6.0",
		Port:  PortKafka,
		Env: map[string]string{
			"KAFKA_NODE_ID":                                  "1",
			"KAFKA_PROCESS_ROLES":                            "broker,controller",
			"KAFKA_LISTENERS":                                "PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093",
			"KAFKA_ADVERTISED_LISTENERS":                     "PLAINTEXT://kafka:9092",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":           "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
			"KAFKA_CONTROLLER_LISTENER_NAMES":                "CONTROLLER",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                 "1@kafka:9093",
			"KAFKA_INTER_BROKER_LISTENER_NAME":               "PLAINTEXT",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":            "1",
			"KAFKA_LOG_DIRS":                                 "/var/lib/kafka/data",
			"CLUSTER_ID":                                     "MkU3OEVBNTcwNTJENDM2Qk",
		},
		Volumes: []string{"kafka_data:/var/lib/kafka/data"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "kafka-broker-api-versions --bootstrap-server localhost:9092 >/dev/null 2>&1 || exit 1"},
			Interval:    "10s",
			Timeout:     "5s",
			Retries:     10,
			StartPeriod: "15s",
		},
	}
}

// MinIOService returns a MinIO object store.
func MinIOService() *domain.InfraService {
	return &domain.InfraService{
		Type:    domain.InfraMinIO,
		Name:    "minio",
		Image:   "minio/minio:latest",
		Port:    PortMinIO,
		Command: `server /data --console-address ":9001"`,
		Env: map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		},
		Volumes: []string{"minio_data:/data"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "mc ready local || exit 1"},
			Interval:    "5s",
			Timeout:     "3s",
			Retries:     10,
			StartPeriod: "5s",
		},
	}
}

// RabbitMQService returns RabbitMQ with the management plugin.
func RabbitMQService() *domain.InfraService {
	return &domain.InfraService{
		Type:  domain.InfraRabbitMQ,
		Name:  "rabbitmq",
		Image: "rabbitmq:3-management",
		Port:  PortRabbitMQ,
		Env: map[string]string{
			"RABBITMQ_DEFAULT_USER": "app",
			"RABBITMQ_DEFAULT_PASS": "app",
		},
		Volumes: []string{"rabbitmq_data:/var/lib/rabbitmq"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD", "rabbitmq-diagnostics", "-q", "ping"},
			Interval:    "10s",
			Timeout:     "5s",
			Retries:     10,
			StartPeriod: "10s",
		},
	}
}

// ElasticsearchService returns a single-node Elasticsearch.
func ElasticsearchService() *domain.InfraService {
	return &domain.InfraService{
		Type:  domain.InfraElasticsearch,
		Name:  "elasticsearch",
		Image: "docker.elastic.co/elasticsearch/elasticsearch:8.13.4",
		Port:  PortElasticsearch,
		Env: map[string]string{
			"discovery.type":         "single-node",
			"xpack.security.enabled": "false",
			"ES_JAVA_OPTS":           "-Xms512m -Xmx512m",
		},
		Volumes: []string{"elasticsearch_data:/usr/share/elasticsearch/data"},
		Healthcheck: domain.HealthcheckSpec{
			Test:        []string{"CMD-SHELL", "curl -fsS http://localhost:9200/_cluster/health >/dev/null || exit 1"},
			Interval:    "10s",
			Timeout:     "5s",
			Retries:     10,
			StartPeriod: "30s",
		},
	}
}

func startsPostgres(v string) bool {
	lv := strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(lv, "postgres://") || strings.HasPrefix(lv, "postgresql://") || strings.HasPrefix(lv, "postgresql+")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
