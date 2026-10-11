FROM python:3.13-slim-bookworm
WORKDIR /migrations
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
CMD ["alembic", "-c", "/migrations/alembic.ini", "upgrade", "head"]
