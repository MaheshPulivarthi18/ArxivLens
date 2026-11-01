import os
from pydantic import BaseModel


class AppConfig(BaseModel):
    postgres_url: str = os.getenv("DATABASE_URL", "postgresql://postgres:postgres@localhost:5432/arxivlens")
    kafka_brokers: str = os.getenv("KAFKA_BROKERS", "localhost:29092")
    minio_endpoint: str = os.getenv("MINIO_ENDPOINT", "localhost:9000")
    minio_access_key: str = os.getenv("MINIO_ACCESS_KEY", "minioadmin")
    minio_secret_key: str = os.getenv("MINIO_SECRET_KEY", "minioadmin")
    minio_bucket: str = os.getenv("MINIO_BUCKET", "arxiv-papers")
    minio_secure: bool = os.getenv("MINIO_SECURE", "false").lower() == "true"
    embedding_model: str = os.getenv("EMBEDDING_MODEL", "BAAI/bge-base-en-v1.5")


config = AppConfig()
