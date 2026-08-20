import base64
import binascii
from collections.abc import Callable, Iterator
from typing import Any


PROTOBUF_OVERHEAD_RESERVE_BYTES = 1024 * 1024
MIB_BYTES = 1024 * 1024


class ResourceLimitError(ValueError):
    """请求或解析结果超过 DocReader 资源上限。"""


class ParseCancelledError(RuntimeError):
    """调用方已取消当前解析请求。"""


class ImageBudget:
    """统一累计一份解析结果中的图片数量、单图字节和总字节。"""

    def __init__(self, max_count: int, max_image_bytes: int, max_total_bytes: int):
        self.max_count = int(max_count)
        self.max_image_bytes = int(max_image_bytes)
        self.max_total_bytes = int(max_total_bytes)
        self.count = 0
        self.total_bytes = 0

    @property
    def remaining_count(self) -> int:
        return max(0, self.max_count - self.count)

    @property
    def remaining_total_bytes(self) -> int:
        return max(0, self.max_total_bytes - self.total_bytes)

    def ensure_count(self, additional: int) -> None:
        if additional < 0 or self.count > self.max_count - additional:
            raise ResourceLimitError(
                f"DocReader image count exceeds limit {self.max_count}"
            )

    def add_size(self, image_bytes: int) -> None:
        image_bytes = int(image_bytes)
        self.ensure_count(1)
        if image_bytes < 0 or image_bytes > self.max_image_bytes:
            raise ResourceLimitError(
                f"DocReader image size {image_bytes} bytes exceeds "
                f"limit {self.max_image_bytes} bytes"
            )
        if self.total_bytes > self.max_total_bytes - image_bytes:
            raise ResourceLimitError(
                "DocReader total image bytes exceed limit "
                f"{self.max_total_bytes} bytes"
            )
        self.count += 1
        self.total_bytes += image_bytes

    def add_bytes(self, image: bytes | bytearray | memoryview) -> None:
        self.add_size(len(image))


def grpc_message_limit_bytes(max_file_size_mb: int) -> int:
    """将 DocReader 配置中的 MiB 上限转换为 gRPC 字节上限。"""
    return max(0, int(max_file_size_mb)) * MIB_BYTES


def file_payload_limit(max_message_bytes: int) -> int:
    """返回扣除 protobuf 字段开销后的文件正文上限。"""
    return max(0, max_message_bytes - PROTOBUF_OVERHEAD_RESERVE_BYTES)


def validate_request_limits(
    file_size: int,
    serialized_size: int,
    max_message_bytes: int,
) -> None:
    """在进入解析器前复核文件正文和完整 protobuf 大小。"""
    payload_limit = file_payload_limit(max_message_bytes)
    if file_size > payload_limit:
        raise ResourceLimitError(
            f"DocReader file payload {file_size} bytes exceeds "
            f"limit {payload_limit} bytes"
        )
    if serialized_size > max_message_bytes:
        raise ResourceLimitError(
            f"DocReader protobuf request {serialized_size} bytes exceeds "
            f"gRPC limit {max_message_bytes} bytes"
        )


def _decode_image(value: Any, max_image_bytes: int) -> bytes:
    if isinstance(value, str):
        encoded = value
        if encoded.startswith("data:") and "," in encoded:
            encoded = encoded.split(",", 1)[1]
        try:
            encoded_bytes = encoded.strip().encode("ascii")
        except UnicodeEncodeError as exc:
            raise ResourceLimitError("DocReader image contains invalid base64 data") from exc

        encoded_bytes += b"=" * (-len(encoded_bytes) % 4)
        padding = len(encoded_bytes) - len(encoded_bytes.rstrip(b"="))
        estimated_size = len(encoded_bytes) // 4 * 3 - padding
        if estimated_size > max_image_bytes:
            raise ResourceLimitError(
                f"DocReader image size exceeds limit {max_image_bytes} bytes"
            )
        try:
            image_bytes = base64.b64decode(encoded_bytes, validate=True)
        except (binascii.Error, ValueError) as exc:
            raise ResourceLimitError("DocReader image contains invalid base64 data") from exc
    elif isinstance(value, (bytes, bytearray, memoryview)):
        image_bytes = bytes(value)
    else:
        raise ResourceLimitError("DocReader image data has an unsupported type")

    if len(image_bytes) > max_image_bytes:
        raise ResourceLimitError(
            f"DocReader image size {len(image_bytes)} bytes exceeds "
            f"limit {max_image_bytes} bytes"
        )
    return image_bytes


def _estimate_image_size(value: Any) -> int:
    """不解码图片即可得到严格的解码后字节估算，用于前置拒绝。"""
    if isinstance(value, str):
        encoded = value
        if encoded.startswith("data:") and "," in encoded:
            encoded = encoded.split(",", 1)[1]
        encoded = encoded.strip()
        if not encoded.isascii():
            raise ResourceLimitError("DocReader image contains invalid base64 data")
        encoded += "=" * (-len(encoded) % 4)
        padding = len(encoded) - len(encoded.rstrip("="))
        return len(encoded) // 4 * 3 - padding
    if isinstance(value, (bytes, bytearray, memoryview)):
        return len(value)
    raise ResourceLimitError("DocReader image data has an unsupported type")


def iter_limited_image_data(
    images: dict,
    max_count: int,
    max_image_bytes: int,
    max_total_bytes: int,
    is_cancelled: Callable[[], bool] | None = None,
) -> Iterator[tuple[str, bytes]]:
    """逐张解码图片，并同时执行取消与三类资源上限检查。"""
    budget = ImageBudget(max_count, max_image_bytes, max_total_bytes)
    for value in images.values():
        if is_cancelled is not None and is_cancelled():
            raise ParseCancelledError("DocReader request was cancelled")
        budget.add_size(_estimate_image_size(value))

    for ref_path in list(images.keys()):
        if is_cancelled is not None and is_cancelled():
            raise ParseCancelledError("DocReader request was cancelled")
        value = images[ref_path]
        image_bytes = _decode_image(value, max_image_bytes)
        images.pop(ref_path)
        yield str(ref_path), image_bytes
