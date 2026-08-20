"""DocReader 统一日志凭据脱敏。

保留文件名、URL、图片 URL、解析结果和业务正文；仅遮蔽认证凭据，以及明确
不应写入日志的内联 Base64 载荷。模块通过 LogRecordFactory 覆盖普通消息、
异常正文和堆栈，避免各解析器分别实现不一致的过滤逻辑。
"""

from __future__ import annotations

import logging
import re
import traceback
from typing import Any

_REDACTED = "[REDACTED]"
_SECRET_FIELD_NAMES = (
    r"(?:password|passwd|(?:new|old|current)[_-]?password|authorization|"
    r"proxy[_-]?authorization|authorization[_-]?attempt|token|id[_-]?token|"
    r"session[_-]?token|private[_-]?token|publish[_-]?token|invite[_-]?token|"
    r"invitation[_-]?token|verification[_-]?token|reset[_-]?token|csrf[_-]?token|"
    r"[a-z0-9_-]*api[_ -]?key|x[_-]?api[_-]?key|api[_-]?secret|api[_-]?token|"
    r"auth(?:entication|orization)?[_-]?token|access[_-]?token|refresh[_-]?token|"
    r"access[_-]?key(?:[_-]?id)?|secret[_-]?id|jwt[_-]?secret|"
    r"system[_-]?aes[_-]?key|aes[_-]?key|encryption[_-]?key|app[_-]?secret|"
    r"client[_-]?secret|hmac[_-]?secret|(?:[a-z0-9]+[_-])*secret[_-]?access[_-]?key|"
    r"secret[_-]?key|private[_-]?key|signature|sig|secret)"
)
_FIELD_PREFIX = r"(^|[\s,{\[?&;；.(])"
_SECRET_FIELD_RE = re.compile(rf"^{_SECRET_FIELD_NAMES}$", re.IGNORECASE)

_PATTERNS: tuple[tuple[re.Pattern[str], str], ...] = (
    (
        re.compile(
            rf"{_FIELD_PREFIX}([\"']?(?:authorization|proxy[_-]?authorization)"
            rf"[\"']?\s*[:=]\s*)(?:Bearer|Basic)\s+[A-Za-z0-9._~+/-]+=*",
            re.IGNORECASE,
        ),
        rf"\1\2{_REDACTED}",
    ),
    (
        re.compile(r"(\bBearer\s+)[A-Za-z0-9._~+/-]+=*", re.IGNORECASE),
        rf"\1{_REDACTED}",
    ),
    (
        re.compile(r"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b"),
        "[REDACTED_JWT]",
    ),
    (re.compile(r"\bsk-[A-Za-z0-9_-]{8,}\b"), "[REDACTED_API_KEY]"),
    (
        re.compile(
            r"\b(?:github_pat_[A-Za-z0-9_]{8,}|gh[pousr]_[A-Za-z0-9]{8,}|"
            r"xox[baprs]-[A-Za-z0-9-]{8,}|AIza[A-Za-z0-9_-]{20,}|"
            r"ya29\.[A-Za-z0-9_-]{8,})\b"
        ),
        "[REDACTED_PROVIDER_TOKEN]",
    ),
    (
        re.compile(r"([a-z][a-z0-9+.-]*://[^:/@\s]+:)[^/@\s]+@", re.IGNORECASE),
        rf"\1{_REDACTED}@",
    ),
    (
        re.compile(r"([a-z][a-z0-9+.-]*://)[^/:@\s]+@", re.IGNORECASE),
        rf"\1{_REDACTED}@",
    ),
    (
        re.compile(
            rf"{_FIELD_PREFIX}([\"']?{_SECRET_FIELD_NAMES}[\"']?\s*[:=]\s*)"
            r'(")((?:\\.|[^"\\])*)(")',
            re.IGNORECASE,
        ),
        rf"\1\2\3{_REDACTED}\5",
    ),
    (
        re.compile(
            rf"{_FIELD_PREFIX}([\"']?{_SECRET_FIELD_NAMES}[\"']?\s*[:=]\s*)"
            r"(')((?:\\.|[^'\\])*)(')",
            re.IGNORECASE,
        ),
        rf"\1\2\3{_REDACTED}\5",
    ),
    (
        re.compile(
            rf"{_FIELD_PREFIX}([\"']?{_SECRET_FIELD_NAMES}[\"']?\s*[:=]\s*)"
            r"([\"']?)([^\"',}\]&;；\s)]+)([\"']?)",
            re.IGNORECASE,
        ),
        rf"\1\2\3{_REDACTED}\5",
    ),
    (
        re.compile(
            r"-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----",
            re.IGNORECASE | re.DOTALL,
        ),
        "[REDACTED_PRIVATE_KEY]",
    ),
)

_FAILED_BASE64_RE = re.compile(
    r"(Failed to decode base64 image skip it:\s*)(\S+)", re.IGNORECASE
)
_DATA_URI_RE = re.compile(
    r"(data:(?:image|application)/[A-Za-z0-9.+-]+;base64,)([A-Za-z0-9+/=_-]+)",
    re.IGNORECASE,
)


def _base64_replacement(match: re.Match[str]) -> str:
    return f"{match.group(1)}[REDACTED_BASE64 length={len(match.group(2))}]"


def sanitize_log_text(value: Any, *, preserve_newlines: bool = False) -> str:
    """返回保留业务信息、已遮蔽认证秘密的日志文本。"""

    text = str(value)
    text = text.replace("\r", "\\r")
    if not preserve_newlines:
        text = text.replace("\n", " ").replace("\t", " ")
    text = "".join(
        char if char == "\n" or char == "\t" or ord(char) >= 32 else " "
        for char in text
    )
    text = _FAILED_BASE64_RE.sub(_base64_replacement, text)
    text = _DATA_URI_RE.sub(_base64_replacement, text)
    for pattern, replacement in _PATTERNS:
        text = pattern.sub(replacement, text)
    return text


def _sanitize_record(record: logging.LogRecord) -> logging.LogRecord:
    try:
        rendered = record.getMessage()
    except Exception:
        rendered = str(record.msg)
    record.msg = sanitize_log_text(rendered)
    record.args = ()

    if record.exc_info:
        exception_text = "".join(traceback.format_exception(*record.exc_info))
        record.exc_text = sanitize_log_text(exception_text, preserve_newlines=True)
        record.exc_info = None
    elif record.exc_text:
        record.exc_text = sanitize_log_text(record.exc_text, preserve_newlines=True)
    if record.stack_info:
        record.stack_info = sanitize_log_text(record.stack_info, preserve_newlines=True)
    for key, value in tuple(record.__dict__.items()):
        if _SECRET_FIELD_RE.fullmatch(key):
            record.__dict__[key] = _REDACTED
        elif key not in _STANDARD_LOG_RECORD_FIELDS and isinstance(value, str):
            record.__dict__[key] = sanitize_log_text(value)
    return record


def install_log_redaction() -> None:
    """为当前进程安装幂等的日志记录工厂。"""

    current_factory = logging.getLogRecordFactory()
    if getattr(current_factory, "_weknora_redacts_credentials", False):
        return

    def redacting_factory(*args: Any, **kwargs: Any) -> logging.LogRecord:
        return _sanitize_record(current_factory(*args, **kwargs))

    setattr(redacting_factory, "_weknora_redacts_credentials", True)
    logging.setLogRecordFactory(redacting_factory)

    current_format = logging.Formatter.format
    if getattr(current_format, "_weknora_redacts_credentials", False):
        return

    def redacting_format(formatter: logging.Formatter, record: logging.LogRecord) -> str:
        rendered = current_format(formatter, record)
        return sanitize_log_text(rendered, preserve_newlines=True)

    setattr(redacting_format, "_weknora_redacts_credentials", True)
    logging.Formatter.format = redacting_format


_STANDARD_LOG_RECORD_FIELDS = frozenset(logging.makeLogRecord({}).__dict__)
