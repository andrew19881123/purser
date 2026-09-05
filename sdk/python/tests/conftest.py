"""Shared pytest fixtures for the Purser SDK test suite."""
from __future__ import annotations

import pytest
import respx
import httpx


BASE_URL = "http://purser.test"


@pytest.fixture
def base_url() -> str:
    return BASE_URL


@pytest.fixture
def mock_api():
    """Activate the respx mock router for each test."""
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        yield router
