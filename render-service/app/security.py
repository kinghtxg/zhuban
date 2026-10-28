"""docx 安全校验、大小限制、图片下载 SSRF 防护（CONTRACT §3.2 / §3.6）。"""
from __future__ import annotations

import ipaddress
import socket
import zipfile
from io import BytesIO
from typing import List, Tuple
from urllib.parse import urlparse

import requests

from . import config


class SecurityError(Exception):
    """模板/数据/URL 安全校验失败，code 见 CONTRACT §2。"""

    def __init__(self, code: str, message: str, details: List[dict] | None = None, status: int = 400):
        super().__init__(message)
        self.code = code
        self.message = message
        self.details = details or []
        self.status = status


def validate_docx(raw: bytes) -> zipfile.ZipFile:
    """校验上传内容是合法 docx：zip、含 word/document.xml、拒 docm/宏、大小限制。

    抛出 SecurityError：
      - 413 payload_too_large
      - 415 unsupported_media_type
      - 400 template_invalid
    """
    if len(raw) > config.MAX_TEMPLATE_BYTES:
        raise SecurityError(
            "payload_too_large",
            f"模板文件超过 {config.MAX_TEMPLATE_MB}MB 上限",
            status=413,
        )
    if not raw or len(raw) < 4:
        raise SecurityError("unsupported_media_type", "上传内容不是有效的 docx 文件", status=415)
    try:
        zf = zipfile.ZipFile(BytesIO(raw))
    except zipfile.BadZipFile:
        raise SecurityError("unsupported_media_type", "上传内容不是有效的 docx（zip 损坏）", status=415)

    names = zf.namelist()
    if "word/document.xml" not in names:
        raise SecurityError(
            "unsupported_media_type",
            "文件缺少 word/document.xml，不是有效的 Word 文档",
            status=415,
        )

    # 拒 docm（宏文档）
    ctypes = zf.read("[Content_Types].xml").decode("utf-8", "ignore") if "[Content_Types].xml" in names else ""
    if "wordprocessingml.document.macroEnabled" in ctypes or "vbaProject" in ctypes:
        raise SecurityError(
            "unsupported_media_type",
            "拒绝含宏的 docm/vbaProject 模板，请另存为 .docx",
            status=415,
        )
    for n in names:
        if n == "word/vbaProject.bin" or n.endswith("/vbaProject.bin"):
            raise SecurityError(
                "unsupported_media_type",
                "拒绝含宏部件 vbaProject.bin 的模板，请另存为 .docx",
                status=415,
            )
    return zf


def _is_private_ip(ip: str) -> bool:
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return True
    return addr.is_private or addr.is_loopback or addr.is_link_local or addr.is_multicast or addr.is_unspecified


def assert_safe_url(url: str) -> str:
    """校验图片/二维码 URL：仅 http/https，拒绝内网/回环（除非 IMAGE_ALLOW_PRIVATE）。"""
    p = urlparse(url)
    if p.scheme not in ("http", "https"):
        raise SecurityError(
            "data_invalid",
            f"图片 URL 仅支持 http/https，收到 {p.scheme!r}",
            status=422,
        )
    host = p.hostname
    if not host:
        raise SecurityError("data_invalid", "图片 URL 缺少主机名", status=422)
    if not config.IMAGE_ALLOW_PRIVATE:
        try:
            infos = socket.getaddrinfo(host, p.port or (443 if p.scheme == "https" else 80))
        except socket.gaierror:
            raise SecurityError("data_invalid", f"无法解析图片主机 {host}", status=422)
        ips = {ai[4][0] for ai in infos}
        if any(_is_private_ip(ip) for ip in ips):
            raise SecurityError(
                "data_invalid",
                f"拒绝下载内网/回环地址的图片（{host}）",
                status=422,
            )
    return url


def download_image(url: str) -> bytes:
    """下载图片：超时 10s、重定向 ≤2、大小 ≤ MAX_IMAGE_MB。支持 data: URI。"""
    if url.startswith("data:"):
        # data:image/png;base64,xxxx
        try:
            header, b64 = url.split(",", 1)
            import base64

            if not header.startswith("data:image/"):
                raise ValueError("not image")
            data = base64.b64decode(b64)
        except Exception:
            raise SecurityError("data_invalid", "data URI 图片解析失败", status=422)
        if len(data) > config.MAX_IMAGE_BYTES:
            raise SecurityError("payload_too_large", "data URI 图片超过 5MB", status=413)
        return data

    assert_safe_url(url)
    try:
        resp = requests.get(url, timeout=10, allow_redirects=True, stream=True)
    except requests.RequestException as e:
        raise SecurityError("data_invalid", f"图片下载失败：{e}", status=422)
    # 限制重定向次数由 requests 最大重定向控制
    if len(resp.history) > 2:
        raise SecurityError("data_invalid", "图片下载重定向超过 2 次", status=422)
    if resp.status_code != 200:
        raise SecurityError("data_invalid", f"图片下载失败：HTTP {resp.status_code}", status=422)
    data = resp.content
    if len(data) > config.MAX_IMAGE_BYTES:
        raise SecurityError("payload_too_large", "图片超过 5MB 上限", status=413)
    return data
