import logging
import multiprocessing
import os
import signal
import threading
import traceback
from contextlib import contextmanager
from collections.abc import Callable
from typing import Dict, Iterator

from docreader.limits import ParseCancelledError

logger = logging.getLogger(__name__)

_LIMITERS: Dict[str, threading.BoundedSemaphore] = {}
_LIMITERS_LOCK = threading.Lock()


def _get_limiter(name: str, max_workers: int) -> threading.BoundedSemaphore:
    with _LIMITERS_LOCK:
        limiter = _LIMITERS.get(name)
        if limiter is None:
            limiter = threading.BoundedSemaphore(max_workers)
            _LIMITERS[name] = limiter
        return limiter


def _raise_if_cancelled(is_cancelled: Callable[[], bool] | None) -> None:
    if is_cancelled is not None and is_cancelled():
        raise ParseCancelledError("DocReader request was cancelled")


@contextmanager
def cancellable_lock(
    lock: threading.Lock,
    is_cancelled: Callable[[], bool] | None = None,
) -> Iterator[None]:
    """轮询获取进程锁，使 gRPC 取消能中止等待。"""
    while not lock.acquire(timeout=0.1):
        _raise_if_cancelled(is_cancelled)
    try:
        _raise_if_cancelled(is_cancelled)
        yield
    finally:
        lock.release()


@contextmanager
def parser_worker_limit(
    name: str,
    max_workers: int,
    is_cancelled: Callable[[], bool] | None = None,
) -> Iterator[None]:
    """Limit concurrent access to heavy, process-wide parser operations.

    Set max_workers <= 0 to disable throttling for deployments that have enough
    CPU/GPU resources and know the parser backend is safe under concurrency.
    """

    if max_workers <= 0:
        _raise_if_cancelled(is_cancelled)
        yield
        _raise_if_cancelled(is_cancelled)
        return

    limiter = _get_limiter(name, max_workers)
    logger.debug("Waiting for %s parser slot (max_workers=%d)", name, max_workers)
    while not limiter.acquire(timeout=0.1):
        _raise_if_cancelled(is_cancelled)
    try:
        _raise_if_cancelled(is_cancelled)
        yield
    finally:
        limiter.release()


def terminate_process_pool(executor) -> None:
    """终止进程池中已运行和待运行任务，避免取消后继续消耗 CPU。"""
    if executor is None:
        return
    terminate_workers = getattr(executor, "terminate_workers", None)
    if callable(terminate_workers):
        terminate_workers()
        return

    # Python 3.10 没有公开的 terminate_workers；只在丢弃整个进程池时使用兼容路径。
    processes = list((getattr(executor, "_processes", None) or {}).values())
    for process in processes:
        if process.is_alive():
            process.terminate()
    for process in processes:
        process.join(timeout=1)
        if process.is_alive() and hasattr(process, "kill"):
            process.kill()
            process.join(timeout=1)
    executor.shutdown(wait=False, cancel_futures=True)


def _cancellable_process_entry(connection, target, args, kwargs) -> None:
    if os.name != "nt":
        try:
            os.setsid()
        except OSError:
            pass
    try:
        connection.send(("ok", target(*args, **kwargs)))
    except BaseException as exc:
        connection.send(
            (
                "error",
                exc.__class__.__name__,
                str(exc),
                traceback.format_exc(),
            )
        )
    finally:
        connection.close()


def _terminate_process(process) -> None:
    if process is None or not process.is_alive():
        return
    if os.name != "nt":
        try:
            process_group = os.getpgid(process.pid)
            if process_group == process.pid:
                os.killpg(process_group, signal.SIGTERM)
            else:
                process.terminate()
        except (OSError, ProcessLookupError):
            process.terminate()
    else:
        process.terminate()
    process.join(timeout=2)
    if process.is_alive():
        if hasattr(process, "kill"):
            process.kill()
        else:
            process.terminate()
        process.join(timeout=2)


def run_in_cancellable_process(
    target,
    *args,
    is_cancelled: Callable[[], bool] | None = None,
    **kwargs,
):
    """在可终止的独立进程运行第三方转换，并轮询调用方取消状态。"""
    _raise_if_cancelled(is_cancelled)
    if is_cancelled is None:
        return target(*args, **kwargs)

    process_context = multiprocessing.get_context("spawn")
    receiver, sender = process_context.Pipe(duplex=False)
    process = process_context.Process(
        target=_cancellable_process_entry,
        args=(sender, target, args, kwargs),
    )
    process.start()
    sender.close()
    try:
        while True:
            _raise_if_cancelled(is_cancelled)
            if receiver.poll(0.1):
                message = receiver.recv()
                process.join(timeout=2)
                if process.is_alive():
                    _terminate_process(process)
                    raise RuntimeError("DocReader parser child process did not exit")
                if message[0] == "ok":
                    return message[1]
                _, error_type, error_message, child_traceback = message
                raise RuntimeError(
                    f"DocReader parser child failed: {error_type}: {error_message}\n"
                    f"{child_traceback}"
                )
            if not process.is_alive():
                if receiver.poll():
                    continue
                raise RuntimeError(
                    "DocReader parser child exited without a result "
                    f"(exit_code={process.exitcode})"
                )
    except ParseCancelledError:
        _terminate_process(process)
        raise
    finally:
        receiver.close()
        if process.is_alive():
            _terminate_process(process)
