import base64
import io
import threading
import time
import unittest
import uuid
from unittest.mock import patch

from PIL import Image

from docreader.limits import ImageBudget, ParseCancelledError, ResourceLimitError
from docreader.models.document import Document
from docreader.parser.base_parser import BaseParser
from docreader.parser.chain_parser import FirstParser
from docreader.parser.doc_parser import SandboxExecutor
from docreader.parser.concurrency import (
    cancellable_lock,
    parser_worker_limit,
    run_in_cancellable_process,
)
from docreader.parser.pdf_parser import (
    PDFParser,
    PDFScannedParser,
    _normalize_image_quality,
    _render_scanned_pages,
)


def _slow_child(delay):
    time.sleep(delay)
    return "finished"


class ParserConcurrencyTest(unittest.TestCase):
    def test_first_parser_does_not_swallow_cancellation(self):
        class CancelledParser(BaseParser):
            def parse_into_text(self, _content):
                raise ParseCancelledError("cancelled")

        parser_class = FirstParser.create(CancelledParser)
        with self.assertRaises(ParseCancelledError):
            parser_class().parse_into_text(b"content")

    def test_parser_worker_limit_serializes_work(self):
        limiter_name = f"test-{uuid.uuid4()}"
        active_workers = 0
        max_active_workers = 0
        state_lock = threading.Lock()
        start = threading.Barrier(3)

        def worker():
            nonlocal active_workers, max_active_workers
            start.wait()
            with parser_worker_limit(limiter_name, 1):
                with state_lock:
                    active_workers += 1
                    max_active_workers = max(max_active_workers, active_workers)
                time.sleep(0.02)
                with state_lock:
                    active_workers -= 1

        threads = [threading.Thread(target=worker) for _ in range(2)]
        for thread in threads:
            thread.start()

        start.wait()
        for thread in threads:
            thread.join()

        self.assertEqual(max_active_workers, 1)

    def test_parser_worker_limit_cancels_waiter(self):
        limiter_name = f"cancel-{uuid.uuid4()}"
        cancelled = threading.Event()
        waiting = threading.Event()
        finished = threading.Event()
        errors = []

        def waiter():
            waiting.set()
            try:
                with parser_worker_limit(
                    limiter_name,
                    1,
                    is_cancelled=cancelled.is_set,
                ):
                    self.fail("取消后的等待者不应获得解析槽位")
            except Exception as exc:  # 在线程中回传异常给主测试线程断言
                errors.append(exc)
            finally:
                finished.set()

        with parser_worker_limit(limiter_name, 1):
            thread = threading.Thread(target=waiter)
            thread.start()
            self.assertTrue(waiting.wait(timeout=1))
            time.sleep(0.15)
            cancelled.set()
            self.assertTrue(finished.wait(timeout=1))
            thread.join(timeout=1)

        self.assertEqual(len(errors), 1)
        self.assertIsInstance(errors[0], ParseCancelledError)

    def test_cancellable_lock_rejects_pre_cancelled_request(self):
        lock = threading.Lock()
        with self.assertRaises(ParseCancelledError):
            with cancellable_lock(lock, is_cancelled=lambda: True):
                self.fail("已取消请求不应进入临界区")

    def test_doc_subprocess_rejects_pre_cancelled_request(self):
        executor = SandboxExecutor(is_cancelled=lambda: True)
        with self.assertRaises(ParseCancelledError):
            executor.execute_in_sandbox(["unused-command"])

    def test_isolated_parser_process_is_terminated_on_cancel(self):
        cancelled = threading.Event()
        timer = threading.Timer(0.2, cancelled.set)
        timer.start()
        started = time.monotonic()
        try:
            with self.assertRaises(ParseCancelledError):
                run_in_cancellable_process(
                    _slow_child,
                    5,
                    is_cancelled=cancelled.is_set,
                )
        finally:
            timer.cancel()
        self.assertLess(time.monotonic() - started, 3)

    def test_scanned_pdf_parser_outputs_jpeg_images(self):
        pdf_bytes = io.BytesIO()
        pages = [
            Image.new("RGB", (64, 64), "white"),
            Image.new("RGB", (64, 64), "black"),
        ]
        pages[0].save(
            pdf_bytes,
            format="PDF",
            save_all=True,
            append_images=pages[1:],
        )

        document = PDFScannedParser(file_name="scan.pdf").parse_into_text(
            pdf_bytes.getvalue()
        )

        image_ref = "images/scan_page_1.jpg"
        self.assertIn(f"![scan_page_1.jpg]({image_ref})", document.content)
        self.assertIn(image_ref, document.images)
        self.assertEqual(document.metadata["image_source_type"], "scanned_pdf")
        self.assertEqual(document.metadata["page_count"], 2)
        self.assertEqual(len(document.images), 2)
        self.assertIn("images/scan_page_2.jpg", document.images)
        image_bytes = base64.b64decode(document.images[image_ref])
        self.assertTrue(image_bytes.startswith(b"\xff\xd8"))

    def test_scanned_pdf_rejects_page_count_before_render(self):
        pdf_bytes = io.BytesIO()
        pages = [
            Image.new("RGB", (64, 64), "white"),
            Image.new("RGB", (64, 64), "black"),
        ]
        pages[0].save(
            pdf_bytes,
            format="PDF",
            save_all=True,
            append_images=pages[1:],
        )

        from dataclasses import replace
        from docreader.parser import pdf_parser

        limited = replace(pdf_parser.CONFIG, pdf_max_pages=1)
        with patch.object(pdf_parser, "CONFIG", limited):
            with self.assertRaises(ResourceLimitError):
                PDFScannedParser(file_name="scan.pdf").parse_into_text(
                    pdf_bytes.getvalue()
                )

    def test_pdf_parser_does_not_fallback_after_cancel_or_limit(self):
        parser = PDFParser(file_name="scan.pdf")
        for error in (
            ParseCancelledError("cancelled"),
            ResourceLimitError("limited"),
        ):
            with self.subTest(error=type(error).__name__):
                with patch.object(parser, "_route", side_effect=error), patch.object(
                    PDFScannedParser,
                    "parse_into_text",
                    return_value=Document(content="fallback"),
                ) as fallback:
                    with self.assertRaises(type(error)):
                        parser.parse_into_text(b"pdf")
                    fallback.assert_not_called()

    def test_scanned_pages_share_budget_with_existing_pdf_images(self):
        class FakePDF:
            def __getitem__(self, _index):
                return object()

        from dataclasses import replace
        from docreader.parser import pdf_parser

        budget = ImageBudget(max_count=3, max_image_bytes=3, max_total_bytes=5)
        budget.add_bytes(b"x")
        serial_config = replace(pdf_parser.CONFIG, pdf_render_parallelism=1)
        with patch.object(pdf_parser, "CONFIG", serial_config), patch.object(
            pdf_parser,
            "_render_page_to_jpeg",
            side_effect=[b"abc", b"def"],
        ):
            with self.assertRaises(ResourceLimitError):
                _render_scanned_pages(
                    FakePDF(),
                    b"pdf",
                    [0, 1],
                    1.0,
                    85,
                    2000,
                    budget=budget,
                )

    def test_scanned_pdf_parser_logs_malformed_pdf_without_format_error(self):
        parser = PDFScannedParser(file_name="broken.pdf")

        with self.assertLogs("docreader.parser.pdf_parser", level="ERROR") as logs:
            with self.assertRaises(Exception):
                parser.parse_into_text(b"not a pdf")

        self.assertTrue(
            any("PDFScannedParser failed to parse PDF:" in line for line in logs.output)
        )

    def test_normalize_image_quality_bounds_jpeg_quality(self):
        self.assertEqual(_normalize_image_quality(-1), 1)
        self.assertEqual(_normalize_image_quality(90), 90)
        self.assertEqual(_normalize_image_quality(120), 95)


if __name__ == "__main__":
    unittest.main()
