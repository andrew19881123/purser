"""AsyncPurserClient — asynchronous HTTP client for the Purser management API."""
from __future__ import annotations

import json
from typing import Any, AsyncGenerator

import httpx

from .exceptions import ConflictError, LicenseRequiredError, NotFoundError, PurserError
from .types import (
    APIKey,
    AuditChain,
    AuditEntry,
    AuditLog,
    ClusterHealth,
    Deployment,
    EnterpriseStatus,
    JoinToken,
    KeyUsage,
    Model,
    ModelHealth,
    ModelSpec,
    Plan,
    PlanPreview,
    Node,
)


class AsyncPurserClient:
    """Asynchronous client for the Purser control-plane management API.

    Mirrors :class:`~purser.PurserClient` exactly, but all methods are
    ``async def`` and use ``httpx.AsyncClient`` under the hood.

    Args:
        base_url: Base URL of the Purser control plane, e.g.
            ``"http://localhost:8080"``.  A trailing slash is normalised away.
        api_key: Optional API key sent as ``Authorization: Bearer <key>``.
        timeout: Request timeout in seconds (default ``30.0``).

    Example::

        client = AsyncPurserClient("http://localhost:8080", api_key="psk_...")
        nodes = await client.list_nodes()
        await client.aclose()

        # or as an async context manager (recommended):
        async with AsyncPurserClient("http://localhost:8080") as client:
            health = await client.cluster_health()

        # SSE streaming:
        async with AsyncPurserClient("http://localhost:8080") as client:
            async for snapshot in client.stream_metrics():
                print(snapshot['aggregate_decode_tok_s'])
    """

    def __init__(
        self,
        base_url: str,
        api_key: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key

        headers: dict[str, str] = {}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"

        self._client = httpx.AsyncClient(
            base_url=self._base_url,
            headers=headers,
            timeout=timeout,
        )

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    async def _request(self, method: str, path: str, **kwargs: Any) -> Any:
        """Execute an async HTTP request and return the parsed JSON body.

        Returns ``None`` for 204 No Content responses.
        Raises a typed exception for any non-2xx status.
        """
        response = await self._client.request(method, path, **kwargs)
        self._raise_for_status(response)
        if response.status_code == 204:
            return None
        return response.json()

    def _raise_for_status(self, response: httpx.Response) -> None:
        if response.status_code < 400:
            return

        # Try to extract a structured error payload.
        message: str = response.text
        error_type: str = ""
        feature: str = ""
        try:
            data = response.json()
            error_field = data.get("error", "")
            if isinstance(error_field, dict):
                # 402 shape: {"error": {"message": ..., "feature": ..., "type": ...}}
                message = error_field.get("message", message)
                error_type = error_field.get("type", "")
                feature = error_field.get("feature", "")
            else:
                # Normal shape: {"error": "kind", "message": "..."}
                error_type = error_field
                message = data.get("message", message)
        except Exception:
            pass

        if response.status_code == 404:
            raise NotFoundError(message)
        if response.status_code == 409:
            raise ConflictError(message)
        if response.status_code == 402:
            raise LicenseRequiredError(message, feature=feature)
        raise PurserError(response.status_code, message, error_type)

    # ------------------------------------------------------------------
    # Nodes
    # ------------------------------------------------------------------

    async def list_nodes(self) -> list[Node]:
        """Return all nodes known to the cluster."""
        data = await self._request("GET", "/api/v1/nodes")
        return [Node._from_dict(n) for n in data.get("nodes", [])]

    async def get_node(self, node_id: str) -> Node:
        """Return a single node by ID.

        Raises:
            NotFoundError: If no node with that ID exists.
        """
        data = await self._request("GET", f"/api/v1/nodes/{node_id}")
        return Node._from_dict(data)

    async def drain_node(self, node_id: str) -> None:
        """Cordon a node so no new work is scheduled onto it.

        This marks the node as ``DRAINING``; existing deployments on the node
        are **not** migrated or rebalanced automatically.

        Raises:
            NotFoundError: If no node with that ID exists.
        """
        await self._request("POST", f"/api/v1/nodes/{node_id}/drain")

    async def restart_node(self, node_id: str) -> None:
        """Tear down all active deployments on the node and let the reconciler
        re-provision them.  The node process itself is not rebooted.

        Returns 409 if no active deployments are present; raises
        :exc:`ConflictError`.

        Raises:
            NotFoundError: If no node with that ID exists.
            ConflictError: If there are no active deployments to restart.
        """
        raise NotImplementedError("restart_node is not yet implemented on the server")

    async def delete_node(self, node_id: str) -> None:
        """Decommission a node (lifecycle transition to DECOMMISSIONED).

        The node must have no active deployments; otherwise a
        :exc:`ConflictError` is raised listing the blocking deployment IDs.

        Raises:
            NotFoundError: If no node with that ID exists.
            ConflictError: If the node still hosts active deployments.
        """
        await self._request("DELETE", f"/api/v1/nodes/{node_id}")

    # ------------------------------------------------------------------
    # Models
    # ------------------------------------------------------------------

    async def list_models(self) -> list[Model]:
        """Return all models in the catalog."""
        data = await self._request("GET", "/api/v1/models")
        return [Model._from_dict(m) for m in data.get("models", [])]

    async def create_model(self, spec: ModelSpec) -> Model:
        """Register a new model in the catalog.

        Args:
            spec: A :class:`~purser.ModelSpec` describing the model.

        Returns:
            The newly created :class:`~purser.Model` (fetched with a follow-up
            GET so the full metadata is available).

        Raises:
            ConflictError: If a model with the same ID already exists.
        """
        data = await self._request("POST", "/api/v1/models", json=spec.to_dict())
        # Server returns {"model_id": "..."} on 201 — do a follow-up GET for
        # the full object.
        model_id: str = data.get("model_id", spec.model_id)
        return await self.get_model(model_id)

    async def get_model(self, model_id: str) -> Model:
        """Return a single model by ID.

        Raises:
            NotFoundError: If no model with that ID exists.
        """
        # The server does not have a dedicated GET /models/{id} endpoint, so
        # we list and filter locally.
        models = await self.list_models()
        for m in models:
            if m.id == model_id:
                return m
        raise NotFoundError(f"model not found: {model_id}")

    async def delete_model(self, model_id: str) -> None:
        """Remove a model from the catalog.

        The model must have no active deployments referencing it.

        Raises:
            NotFoundError: If no model with that ID exists.
            ConflictError: If active deployments still reference the model.
        """
        await self._request("DELETE", f"/api/v1/models/{model_id}")

    async def preview_plan(self, model_id: str) -> PlanPreview:
        """Compute a deployment plan dry-run without persisting or deploying.

        Returns:
            A :class:`~purser.PlanPreview` with ``feasible=True`` and the
            plan details when the model fits the current fleet, or
            ``feasible=False`` and a ``reason`` when it does not.

        Raises:
            NotFoundError: If no model with that ID exists.
        """
        data = await self._request("POST", f"/api/v1/models/{model_id}/plan")
        feasible: bool = bool(data.get("feasible", False))
        if not feasible:
            return PlanPreview(feasible=False, reason=data.get("reason", ""))
        return PlanPreview(feasible=True, plan=Plan._from_dict(data))

    async def deploy_model(
        self,
        model_id: str,
        plan_id: str | None = None,
    ) -> Deployment:
        """Deploy a model to the fleet.

        The planner computes a new plan automatically unless *plan_id* is
        supplied (referencing a plan previously returned by
        :meth:`preview_plan` or :meth:`get_plan`).

        Args:
            model_id: ID of the model to deploy.
            plan_id: Optional stored plan ID to use instead of letting the
                planner create a fresh one.

        Returns:
            A :class:`~purser.Deployment` in the ``provisioning`` state.

        Raises:
            NotFoundError: If the model or plan ID does not exist.
        """
        body: dict[str, Any] = {}
        if plan_id:
            body["plan_id"] = plan_id
        data = await self._request(
            "POST", f"/api/v1/models/{model_id}/deploy", json=body
        )
        return Deployment(
            id=data.get("deployment_id", ""),
            model_id=data.get("model_id", model_id),
            plan_id=data.get("plan_id", plan_id or ""),
            state="provisioning",
        )

    async def get_model_health(self, model_id: str) -> ModelHealth:
        """Return health information for a deployed model.

        Raises:
            NotFoundError: If no model with that ID exists.
        """
        raise NotImplementedError("get_model_health is not yet implemented on the server")

    # ------------------------------------------------------------------
    # Deployments
    # ------------------------------------------------------------------

    async def list_deployments(self) -> list[Deployment]:
        """Return all deployments (active and historical)."""
        data = await self._request("GET", "/api/v1/deployments")
        return [Deployment._from_dict(d) for d in data.get("deployments", [])]

    async def delete_deployment(self, deployment_id: str) -> None:
        """Tear down a deployment.

        Raises:
            NotFoundError: If no deployment with that ID exists.
        """
        await self._request("DELETE", f"/api/v1/deployments/{deployment_id}")

    async def get_plan(self, plan_id: str) -> Plan:
        """Return a stored deployment plan by ID.

        Raises:
            NotFoundError: If no plan with that ID exists.
        """
        data = await self._request("GET", f"/api/v1/plans/{plan_id}")
        return Plan._from_dict(data)

    # ------------------------------------------------------------------
    # API keys
    # ------------------------------------------------------------------

    async def list_api_keys(self) -> list[APIKey]:
        """Return all API keys (metadata only; plaintext keys are never re-exposed)."""
        data = await self._request("GET", "/api/v1/apikeys")
        return [APIKey._from_dict(k) for k in data.get("apikeys", [])]

    async def create_api_key(
        self,
        name: str,
        tenant: str = "",
        quota: int = 0,
    ) -> APIKey:
        """Mint a new gateway API key.

        The plaintext key (``key`` field on the returned object) is returned
        exactly once and never stored — save it immediately.

        Args:
            name: Human-readable name for the key.
            tenant: Optional tenant tag for multi-tenant environments.
            quota: Optional request quota (0 = unlimited).
        """
        data = await self._request(
            "POST",
            "/api/v1/apikeys",
            json={"name": name, "tenant": tenant, "quota": quota},
        )
        return APIKey._from_dict(data, include_key=True)

    async def delete_api_key(self, key_id: str) -> None:
        """Permanently revoke an API key.

        Raises:
            NotFoundError: If no key with that ID exists.
        """
        await self._request("DELETE", f"/api/v1/apikeys/{key_id}")

    async def get_key_usage(self, key_id: str) -> KeyUsage:
        """Return token usage statistics for an API key.

        Raises:
            NotFoundError: If no key with that ID exists.
        """
        raise NotImplementedError("get_key_usage is not yet implemented on the server")

    # ------------------------------------------------------------------
    # Join token
    # ------------------------------------------------------------------

    async def create_join_token(self, ttl_seconds: int = 86400) -> JoinToken:
        """Mint a single-use, expiring cluster join token.

        Hand the returned token to a new machine via the ``PURSER_JOIN_TOKEN``
        environment variable so the Purser agent can enroll it.

        Args:
            ttl_seconds: Token lifetime in seconds (default 86 400 = 24 h).
        """
        data = await self._request(
            "POST",
            "/api/v1/join-token",
            json={"ttl_seconds": ttl_seconds},
        )
        return JoinToken(
            token=data.get("token", ""),
            expires_at=data.get("expires_at", ""),
            cluster_id=data.get("cluster_id", ""),
        )

    # ------------------------------------------------------------------
    # Cluster
    # ------------------------------------------------------------------

    async def cluster_health(self) -> ClusterHealth:
        """Return a coarse cluster health summary (DB + node counts)."""
        data = await self._request("GET", "/api/v1/cluster/health")
        return ClusterHealth._from_dict(data)

    # ------------------------------------------------------------------
    # Enterprise (requires license)
    # ------------------------------------------------------------------

    async def enterprise_status(self) -> EnterpriseStatus:
        """Return the active edition and license information.

        This endpoint never requires a license — it reports ``"community"``
        when no valid license is present.
        """
        data = await self._request("GET", "/api/v1/enterprise/status")
        return EnterpriseStatus._from_dict(data)

    async def audit_log(self, limit: int = 100) -> AuditLog:
        """Return the most recent audit-log entries with chain verification.

        Args:
            limit: Maximum number of entries to return (default 100).

        Returns:
            An :class:`~purser.AuditLog` containing the entries (ascending
            seq order) and a hash-chain verification summary.

        Raises:
            LicenseRequiredError: If no valid enterprise license with the
                ``"audit"`` feature is active.
        """
        data = await self._request(
            "GET", "/api/v1/enterprise/audit-log", params={"limit": limit}
        )
        entries = [AuditEntry._from_dict(e) for e in data.get("entries", [])]
        chain_raw = data.get("chain", {})
        chain = AuditChain(
            verified=bool(chain_raw.get("verified", False)),
            length=int(chain_raw.get("length", 0)),
            break_info=chain_raw.get("break"),
        )
        return AuditLog(
            feature=data.get("feature", "audit"),
            licensee=data.get("licensee", ""),
            entries=entries,
            chain=chain,
        )

    # ------------------------------------------------------------------
    # Metrics streaming (SSE)
    # ------------------------------------------------------------------

    async def stream_metrics(self) -> AsyncGenerator[dict[str, Any], None]:
        """Async generator — stream real-time node metrics from GET /api/v1/metrics (SSE).

        Yields dicts with keys: at, aggregate_decode_tok_s, nodes (list).
        Each dict corresponds to one SSE event (emitted every ~2 seconds).

        Usage::

            async with AsyncPurserClient("http://localhost:8080") as client:
                async for snapshot in client.stream_metrics():
                    print(snapshot['aggregate_decode_tok_s'])
        """
        async with self._client.stream("GET", "/api/v1/metrics") as response:
            async for line in response.aiter_lines():
                if line.startswith("data:"):
                    data = line[5:].strip()
                    if data and data != "[DONE]":
                        try:
                            yield json.loads(data)
                        except json.JSONDecodeError:
                            pass

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    async def aclose(self) -> None:
        """Close the underlying async HTTP connection pool."""
        await self._client.aclose()

    async def __aenter__(self) -> AsyncPurserClient:
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.aclose()
