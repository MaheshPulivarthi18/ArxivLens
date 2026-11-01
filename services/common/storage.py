import io
from minio import Minio
from services.common.config import config


class StorageClient:
    def __init__(self):
        self.client = Minio(
            endpoint=config.minio_endpoint,
            access_key=config.minio_access_key,
            secret_key=config.minio_secret_key,
            secure=config.minio_secure,
        )
        self.bucket = config.minio_bucket

    def download_pdf_bytes(self, storage_key: str) -> bytes:
        """Download raw PDF bytes from MinIO."""
        response = self.client.get_object(self.bucket, storage_key)
        try:
            return response.read()
        finally:
            response.close()
            response.release_conn()

    def object_exists(self, storage_key: str) -> bool:
        """Check if object exists in MinIO."""
        try:
            self.client.stat_object(self.bucket, storage_key)
            return True
        except Exception:
            return False
