FROM python:3.11-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    curl \
    && rm -rf /var/lib/apt/lists/*

# CPU torch by default (same as CI); override with --build-arg TORCH_INDEX_URL=.../whl/cu121 for CUDA.
ARG TORCH_INDEX_URL=https://download.pytorch.org/whl/cpu
COPY requirements-m0.txt .
RUN pip install --no-cache-dir --extra-index-url ${TORCH_INDEX_URL} -r requirements-m0.txt minio ulid-py

COPY shared/ shared/
COPY scripts/ scripts/
COPY services/ services/

ENV PYTHONPATH=/app

CMD ["python", "services/processor/worker.py"]
