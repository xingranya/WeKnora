import io
import logging

from docreader.log_redaction import install_log_redaction, sanitize_log_text


def test_preserves_business_audit_content_and_redacts_credentials():
    raw = (
        "员工正文=客户年度报价；file=报价单.pdf；"
        "url=https://docs.example.com/方案；image_url=https://cdn.example.com/a.png；"
        "model_result=解析成功；folder_token=folder-123；document_token=doc-456；"
        "mineru_api_key=private-api-key；Authorization: Bearer provider-token"
    )
    sanitized = sanitize_log_text(raw)

    for business_text in (
        "员工正文=客户年度报价",
        "file=报价单.pdf",
        "url=https://docs.example.com/方案",
        "image_url=https://cdn.example.com/a.png",
        "model_result=解析成功",
        "folder_token=folder-123",
        "document_token=doc-456",
    ):
        assert business_text in sanitized
    assert "private-api-key" not in sanitized
    assert "provider-token" not in sanitized


def test_redacts_failed_image_base64_but_keeps_image_context():
    payload = "aGVsbG8=" * 16
    sanitized = sanitize_log_text(
        f"Failed to decode base64 image skip it: {payload}; "
        f"image_url=data:image/png;base64,{payload}"
    )

    assert payload not in sanitized
    assert "Failed to decode base64 image skip it:" in sanitized
    assert "image_url=data:image/png;base64,[REDACTED_BASE64" in sanitized
    assert "length=" in sanitized


def test_log_record_factory_redacts_message_and_exception_text():
    install_log_redaction()
    output = io.StringIO()
    handler = logging.StreamHandler(output)
    handler.setFormatter(logging.Formatter("%(levelname)s %(message)s"))
    logger = logging.getLogger("docreader.security-test")
    logger.setLevel(logging.INFO)
    logger.propagate = False
    logger.addHandler(handler)
    try:
        logger.info("解析文档 %s api_key=%s", "客户报价单.pdf", "message-secret")
        try:
            raise RuntimeError("供应商解析失败 password=exception-secret")
        except RuntimeError:
            logger.exception("模型解析结果异常，URL=%s", "https://docs.example.com/failure")
    finally:
        logger.removeHandler(handler)

    rendered = output.getvalue()
    for business_text in (
        "客户报价单.pdf",
        "供应商解析失败",
        "模型解析结果异常",
        "https://docs.example.com/failure",
    ):
        assert business_text in rendered
    assert "message-secret" not in rendered
    assert "exception-secret" not in rendered


def test_formatter_redacts_structured_secret_fields_but_keeps_business_ids():
    install_log_redaction()
    output = io.StringIO()
    handler = logging.StreamHandler(output)
    handler.setFormatter(
        logging.Formatter("%(message)s api_key=%(api_key)s folder_token=%(folder_token)s")
    )
    logger = logging.getLogger("docreader.structured-security-test")
    logger.setLevel(logging.INFO)
    logger.propagate = False
    logger.addHandler(handler)
    try:
        logger.info(
            "处理客户文档",
            extra={"api_key": "structured-secret", "folder_token": "folder-business-id"},
        )
    finally:
        logger.removeHandler(handler)

    rendered = output.getvalue()
    assert "处理客户文档" in rendered
    assert "structured-secret" not in rendered
    assert "folder-business-id" in rendered
