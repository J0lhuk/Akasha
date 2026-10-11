"""PostgreSQL catalog, encrypted relay, accounts and Timescale request metrics."""
from alembic import op

revision = '0001_accounts_timescale'
down_revision = None
branch_labels = None
depends_on = None

def upgrade():
    statements = [
        'CREATE EXTENSION IF NOT EXISTS timescaledb',
        'CREATE TABLE spaces(id TEXT PRIMARY KEY,name TEXT NOT NULL,root TEXT NOT NULL)',
        "CREATE TABLE projects(id TEXT PRIMARY KEY,space_id TEXT NOT NULL REFERENCES spaces(id),name TEXT NOT NULL,root TEXT NOT NULL,shared SMALLINT NOT NULL DEFAULT 0 CHECK(shared IN (0,1)))",
        'CREATE UNIQUE INDEX shared_space ON projects(space_id) WHERE shared=1',
        'CREATE TABLE users(id TEXT PRIMARY KEY,name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,admin SMALLINT NOT NULL DEFAULT 0 CHECK(admin IN (0,1)))',
        "CREATE TABLE grants(user_id TEXT NOT NULL REFERENCES users(id),project_id TEXT NOT NULL REFERENCES projects(id),role TEXT NOT NULL CHECK(role IN ('reader','editor','admin')),PRIMARY KEY(user_id,project_id))",
        'CREATE TABLE audit(seq BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,created TEXT NOT NULL,actor TEXT NOT NULL,action TEXT NOT NULL,resource TEXT NOT NULL)',
        'CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL)',
        'CREATE TABLE accounts(user_id TEXT PRIMARY KEY REFERENCES users(id),username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,disabled SMALLINT NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)))',
        'CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),created BIGINT NOT NULL,expires BIGINT NOT NULL)',
        'CREATE INDEX sessions_user ON sessions(user_id)',
        'CREATE INDEX sessions_expiry ON sessions(expires)',
        'CREATE TABLE events(project TEXT NOT NULL REFERENCES projects(id),seq BIGINT NOT NULL,id TEXT NOT NULL,body BYTEA NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(project,seq),UNIQUE(project,id))',
        'CREATE TABLE request_metrics(time TIMESTAMPTZ NOT NULL,actor TEXT NOT NULL,route TEXT NOT NULL,status INTEGER NOT NULL,duration_ms DOUBLE PRECISION NOT NULL,response_bytes BIGINT NOT NULL)',
        "SELECT create_hypertable('request_metrics',by_range('time',INTERVAL '1 day'))",
        'CREATE INDEX request_metrics_actor_time ON request_metrics(actor,time DESC)',
        # Materialize today's chunk during migration. Its first DDL allocation
        # should not compete with the first application metric batch deadline.
        "INSERT INTO request_metrics VALUES(now(),'','/migration',0,0,0)",
        "DELETE FROM request_metrics WHERE actor='' AND route='/migration'",
        "SELECT add_retention_policy('request_metrics',INTERVAL '90 days')",
    ]
    for statement in statements:
        op.execute(statement)

def downgrade():
    # Downgrades destroy history: intentionally require an operator-reviewed
    # database restore rather than silently dropping encrypted project data.
    raise RuntimeError('Use a verified backup restore for this initial schema')
