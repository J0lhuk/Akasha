import os
from alembic import context
from sqlalchemy import create_engine
from psycopg import sql

# Do not interpolate credentials into alembic.ini or print connection URLs.
url = os.environ['AKASHA_DATABASE_URL']
if url.startswith('postgresql://'):
    url = url.replace('postgresql://', 'postgresql+psycopg://', 1)
engine = create_engine(url, hide_parameters=True)
with engine.connect() as connection:
    # Only one migration runner may alter the schema at a time.
    connection.exec_driver_sql('SELECT pg_advisory_lock(1818321267)')
    connection.commit()
    try:
        context.configure(connection=connection)
        with context.begin_transaction():
            context.run_migrations()
        app_password = os.environ.get('AKASHA_APP_DB_PASSWORD')
        if app_password:
            if len(app_password) < 32:
                raise RuntimeError('Application database password must have at least 32 characters')
            try:
                exists = connection.exec_driver_sql("SELECT 1 FROM pg_roles WHERE rolname='akasha_app'").first()
                command = 'ALTER ROLE' if exists else 'CREATE ROLE'
                statement = sql.SQL(command + ' {} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE CONNECTION LIMIT 24 PASSWORD {}').format(sql.Identifier('akasha_app'), sql.Literal(app_password))
                connection.exec_driver_sql(statement.as_string(connection.connection.driver_connection))
                connection.exec_driver_sql('GRANT USAGE ON SCHEMA public TO akasha_app')
                connection.exec_driver_sql('GRANT SELECT,INSERT,UPDATE,DELETE ON spaces,projects,users,grants,audit,settings,accounts,sessions,events,request_metrics TO akasha_app')
                connection.exec_driver_sql('GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO akasha_app')
                connection.exec_driver_sql('GRANT SELECT ON alembic_version TO akasha_app')
                connection.commit()
            except Exception:
                raise RuntimeError('Application database role provisioning failed') from None
    finally:
        connection.exec_driver_sql('SELECT pg_advisory_unlock(1818321267)')
        connection.commit()
