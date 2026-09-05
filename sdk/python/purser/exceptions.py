"""Purser SDK exception hierarchy."""
from __future__ import annotations


class PurserError(Exception):
    """Base exception for all Purser SDK errors.

    Attributes:
        status_code: HTTP status code returned by the server.
        message: Human-readable error message from the server.
        error_type: Machine-readable error kind string (e.g. ``"not_found"``).
    """

    def __init__(self, status_code: int, message: str, error_type: str = "") -> None:
        self.status_code = status_code
        self.message = message
        self.error_type = error_type
        super().__init__(f"[{status_code}] {message}")


class NotFoundError(PurserError):
    """Raised when the server returns HTTP 404.

    The requested resource (node, model, deployment, plan, or API key) does
    not exist.
    """

    def __init__(self, message: str = "not found") -> None:
        super().__init__(404, message, "not_found")


class ConflictError(PurserError):
    """Raised when the server returns HTTP 409.

    Typical causes: creating a model that already exists, or trying to delete a
    node/model that still has active deployments referencing it.
    """

    def __init__(self, message: str = "conflict") -> None:
        super().__init__(409, message, "conflict")


class LicenseRequiredError(PurserError):
    """Raised when the server returns HTTP 402.

    An enterprise license is required for the requested feature.

    Attributes:
        feature: The gated feature name (e.g. ``"audit"``).
    """

    def __init__(
        self, message: str = "enterprise license required", feature: str = ""
    ) -> None:
        self.feature = feature
        super().__init__(402, message, "license_required")
