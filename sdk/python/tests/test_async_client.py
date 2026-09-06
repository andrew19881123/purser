"""Unit tests for AsyncPurserClient — all HTTP calls are intercepted by respx."""
from __future__ import annotations

import json

import httpx
import pytest
import respx

from purser import AsyncPurserClient, NotFoundError, Node

BASE_URL = "http://purser.test"

# Shared payload fixtures (mirrors those in test_client.py)
NODE_PAYLOAD = {
    "id": "node-abc123",
    "hostname": "gpu-01",
    "os": "linux",
    "arch": "amd64",
    "ram_gb": 64.0,
    "vram_gb": 24.0,
    "state": "NODE_STATE_READY",
    "advertised_inference_addr": "gpu-01:9090",
    "created_at": "2024-01-15T10:00:00Z",
    "updated_at": "2024-01-15T10:05:00Z",
}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _sse_body(*events: dict) -> str:
    """Build a minimal SSE response body from a list of JSON-serialisable dicts."""
    lines = []
    for event in events:
        lines.append(f"data: {json.dumps(event)}")
        lines.append("")  # blank line terminates each event
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# test_async_list_nodes
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_async_list_nodes() -> None:
    """AsyncPurserClient.list_nodes() returns the same Node objects as the sync client."""
    async with AsyncPurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/nodes").mock(
                return_value=httpx.Response(200, json={"nodes": [NODE_PAYLOAD]})
            )
            nodes = await client.list_nodes()

    assert len(nodes) == 1
    n = nodes[0]
    assert isinstance(n, Node)
    assert n.id == "node-abc123"
    assert n.hostname == "gpu-01"
    assert n.os == "linux"
    assert n.arch == "amd64"
    assert n.ram_gb == 64.0
    assert n.vram_gb == 24.0
    assert n.state == "NODE_STATE_READY"
    assert n.advertised_inference_addr == "gpu-01:9090"
    assert n.created_at is not None


@pytest.mark.asyncio
async def test_async_list_nodes_empty() -> None:
    """Empty nodes list is returned correctly."""
    async with AsyncPurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/nodes").mock(
                return_value=httpx.Response(200, json={"nodes": []})
            )
            nodes = await client.list_nodes()

    assert nodes == []


# ---------------------------------------------------------------------------
# test_async_404_raises_not_found
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_async_404_raises_not_found() -> None:
    """A 404 response from any endpoint raises NotFoundError."""
    async with AsyncPurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/nodes/missing").mock(
                return_value=httpx.Response(
                    404, json={"error": "not_found", "message": "node not found"}
                )
            )
            with pytest.raises(NotFoundError) as exc_info:
                await client.get_node("missing")

    err = exc_info.value
    assert err.status_code == 404
    assert "node not found" in str(err)


# ---------------------------------------------------------------------------
# test_stream_metrics_parses_sse (sync version)
# ---------------------------------------------------------------------------


def test_stream_metrics_parses_sse() -> None:
    """Sync stream_metrics() parses two SSE events and yields both as dicts."""
    event1 = {"at": "2024-01-15T10:00:00Z", "aggregate_decode_tok_s": 42.5, "nodes": []}
    event2 = {"at": "2024-01-15T10:00:02Z", "aggregate_decode_tok_s": 39.1, "nodes": []}

    sse_text = _sse_body(event1, event2)

    from purser import PurserClient

    with PurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/metrics").mock(
                return_value=httpx.Response(200, text=sse_text)
            )
            snapshots = list(client.stream_metrics())

    assert len(snapshots) == 2
    assert snapshots[0]["aggregate_decode_tok_s"] == 42.5
    assert snapshots[1]["aggregate_decode_tok_s"] == 39.1
    assert snapshots[0]["at"] == "2024-01-15T10:00:00Z"


def test_stream_metrics_skips_done_sentinel() -> None:
    """[DONE] lines and blank lines are silently ignored."""
    event1 = {"at": "2024-01-15T10:00:00Z", "aggregate_decode_tok_s": 10.0, "nodes": []}
    # Build body manually to insert a [DONE] sentinel
    sse_text = f"data: {json.dumps(event1)}\n\ndata: [DONE]\n\n"

    from purser import PurserClient

    with PurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/metrics").mock(
                return_value=httpx.Response(200, text=sse_text)
            )
            snapshots = list(client.stream_metrics())

    assert len(snapshots) == 1
    assert snapshots[0]["aggregate_decode_tok_s"] == 10.0


def test_stream_metrics_skips_invalid_json() -> None:
    """Malformed JSON in a data: line is silently skipped; other events still yield."""
    event_good = {"at": "2024-01-15T10:00:00Z", "aggregate_decode_tok_s": 5.0, "nodes": []}
    sse_text = f"data: not-valid-json\n\ndata: {json.dumps(event_good)}\n\n"

    from purser import PurserClient

    with PurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/metrics").mock(
                return_value=httpx.Response(200, text=sse_text)
            )
            snapshots = list(client.stream_metrics())

    assert len(snapshots) == 1
    assert snapshots[0]["aggregate_decode_tok_s"] == 5.0


# ---------------------------------------------------------------------------
# test_async_stream_metrics
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_async_stream_metrics() -> None:
    """Async stream_metrics() parses two SSE events and yields both as dicts."""
    event1 = {"at": "2024-01-15T10:00:00Z", "aggregate_decode_tok_s": 42.5, "nodes": []}
    event2 = {"at": "2024-01-15T10:00:02Z", "aggregate_decode_tok_s": 39.1, "nodes": []}

    sse_text = _sse_body(event1, event2)

    async with AsyncPurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/metrics").mock(
                return_value=httpx.Response(200, text=sse_text)
            )
            snapshots: list[dict] = []
            async for snapshot in client.stream_metrics():
                snapshots.append(snapshot)

    assert len(snapshots) == 2
    assert snapshots[0]["aggregate_decode_tok_s"] == 42.5
    assert snapshots[1]["aggregate_decode_tok_s"] == 39.1
    assert snapshots[1]["at"] == "2024-01-15T10:00:02Z"


@pytest.mark.asyncio
async def test_async_stream_metrics_skips_done_sentinel() -> None:
    """Async stream_metrics() silently drops [DONE] sentinel lines."""
    event1 = {"at": "2024-01-15T10:00:00Z", "aggregate_decode_tok_s": 10.0, "nodes": []}
    sse_text = f"data: {json.dumps(event1)}\n\ndata: [DONE]\n\n"

    async with AsyncPurserClient(BASE_URL) as client:
        with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
            router.get("/api/v1/metrics").mock(
                return_value=httpx.Response(200, text=sse_text)
            )
            snapshots: list[dict] = []
            async for snapshot in client.stream_metrics():
                snapshots.append(snapshot)

    assert len(snapshots) == 1


@pytest.mark.asyncio
async def test_async_context_manager() -> None:
    """AsyncPurserClient can be used as an async context manager."""
    async with AsyncPurserClient(BASE_URL) as client:
        assert isinstance(client, AsyncPurserClient)
    # aclose() is called on __aexit__; no assertion needed beyond no crash.


@pytest.mark.asyncio
async def test_async_trailing_slash_normalised() -> None:
    """A base_url with a trailing slash is silently stripped."""
    async with AsyncPurserClient("http://purser.test/") as client:
        assert client._base_url == "http://purser.test"
