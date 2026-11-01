FROM python:3.11-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY requirements-m0.txt .
RUN pip install --no-cache-dir -r requirements-m0.txt minio ulid-py fastapi uvicorn

COPY shared/ shared/
COPY scripts/ scripts/
COPY services/ services/

ENV PYTHONPATH=/app

CMD ["python", "services/embedder/worker.py"]
