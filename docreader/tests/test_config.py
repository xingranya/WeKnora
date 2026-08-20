import os
import unittest
from unittest.mock import patch

from docreader import config


class DocReaderConfigTest(unittest.TestCase):
    def test_parser_concurrency_defaults_are_conservative(self):
        with patch.dict(os.environ, {}, clear=True):
            cfg = config.load_config()

        self.assertEqual(cfg.markitdown_max_workers, 1)
        self.assertEqual(cfg.grpc_max_message_bytes, 50 * 1024 * 1024)
        self.assertEqual(cfg.pdf_render_max_workers, 1)
        self.assertEqual(cfg.pdf_max_pages, 1000)
        self.assertEqual(cfg.pdf_render_dpi, 200)
        self.assertEqual(cfg.pdf_jpeg_quality, 85)
        self.assertEqual(cfg.max_image_count, 256)
        self.assertEqual(cfg.max_image_size_bytes, 16 * 1024 * 1024)
        self.assertEqual(cfg.max_total_image_size_bytes, 128 * 1024 * 1024)

    def test_loads_parser_concurrency_env(self):
        env = {
            "DOCREADER_MARKITDOWN_MAX_WORKERS": "3",
            "DOCREADER_PDF_RENDER_MAX_WORKERS": "2",
            "DOCREADER_PDF_MAX_PAGES": "300",
            "DOCREADER_PDF_RENDER_DPI": "180",
            "DOCREADER_PDF_JPEG_QUALITY": "85",
            "DOCREADER_MAX_IMAGE_COUNT": "12",
            "DOCREADER_MAX_IMAGE_SIZE_MB": "4",
            "DOCREADER_MAX_TOTAL_IMAGE_SIZE_MB": "24",
        }
        with patch.dict(os.environ, env):
            cfg = config.load_config()

        self.assertEqual(cfg.markitdown_max_workers, 3)
        self.assertEqual(cfg.pdf_render_max_workers, 2)
        self.assertEqual(cfg.pdf_max_pages, 300)
        self.assertEqual(cfg.pdf_render_dpi, 180)
        self.assertEqual(cfg.pdf_jpeg_quality, 85)
        self.assertEqual(cfg.max_image_count, 12)
        self.assertEqual(cfg.max_image_size_bytes, 4 * 1024 * 1024)
        self.assertEqual(cfg.max_total_image_size_bytes, 24 * 1024 * 1024)

    def test_dump_config_includes_parser_limits(self):
        dumped = config.dump_config()

        self.assertIn("DOCREADER_MARKITDOWN_MAX_WORKERS", dumped)
        self.assertIn("DOCREADER_PDF_RENDER_MAX_WORKERS", dumped)
        self.assertIn("DOCREADER_PDF_MAX_PAGES", dumped)
        self.assertIn("DOCREADER_PDF_RENDER_DPI", dumped)
        self.assertIn("DOCREADER_PDF_JPEG_QUALITY", dumped)
        self.assertIn("DOCREADER_MAX_IMAGE_COUNT", dumped)
        self.assertIn("DOCREADER_MAX_IMAGE_SIZE_BYTES", dumped)
        self.assertIn("DOCREADER_MAX_TOTAL_IMAGE_SIZE_BYTES", dumped)

    def test_invalid_hard_limits_fall_back_to_safe_defaults(self):
        env = {
            "DOCREADER_GRPC_MAX_WORKERS": "0",
            "DOCREADER_GRPC_MAX_FILE_SIZE_MB": "-1",
            "DOCREADER_MAX_IMAGE_COUNT": "0",
            "DOCREADER_MAX_IMAGE_SIZE_MB": "bad",
            "DOCREADER_MAX_TOTAL_IMAGE_SIZE_MB": "-1",
            "DOCREADER_PDF_MAX_PAGES": "0",
        }
        with patch.dict(os.environ, env, clear=True):
            cfg = config.load_config()

        self.assertEqual(cfg.grpc_max_workers, 4)
        self.assertEqual(cfg.grpc_max_message_bytes, 50 * 1024 * 1024)
        self.assertEqual(cfg.max_image_count, 256)
        self.assertEqual(cfg.max_image_size_bytes, 16 * 1024 * 1024)
        self.assertEqual(cfg.max_total_image_size_bytes, 128 * 1024 * 1024)
        self.assertEqual(cfg.pdf_max_pages, 1000)


if __name__ == "__main__":
    unittest.main()
