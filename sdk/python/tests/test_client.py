"""Unit tests for PurserClient — all HTTP calls are intercepted by respx."""
from __future__ import annotations

import json

import httpx
import pytest
import respx

from purser import (
    PurserClient,
    ConflictError,
    LicenseRequiredError,
    NotFoundError,
    PurserError,
    Node,
    Model,
    ModelSpec,
    Deployment,
    APIKey,
    Plan,
    PlanPreview,
    JoinToken,
    ClusterHealth,
    EnterpriseStatus,
    AuditLog,
)

BASE_URL = "http://purser.test"


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def client():
    with PurserClient(BASE_URL) as c:
        yield c


@pytest.fixture
def client_with_key():
    with PurserClient(BASE_URL, api_key="psk_testkey") as c:
        yield c


# ---------------------------------------------------------------------------
# Nodes
# ---------------------------------------------------------------------------


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


def test_list_nodes_returns_node_objects(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes").mock(
            return_value=httpx.Response(200, json={"nodes": [NODE_PAYLOAD]})
        )
        nodes = client.list_nodes()

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


def test_list_nodes_empty(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes").mock(
            return_value=httpx.Response(200, json={"nodes": []})
        )
        nodes = client.list_nodes()

    assert nodes == []


def test_get_node_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes/node-abc123").mock(
            return_value=httpx.Response(200, json=NODE_PAYLOAD)
        )
        node = client.get_node("node-abc123")

    assert node.id == "node-abc123"
    assert node.state == "NODE_STATE_READY"


def test_get_node_not_found(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes/missing").mock(
            return_value=httpx.Response(
                404, json={"error": "not_found", "message": "node not found"}
            )
        )
        with pytest.raises(NotFoundError) as exc_info:
            client.get_node("missing")

    assert exc_info.value.status_code == 404
    assert "node not found" in str(exc_info.value)


def test_drain_node_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/nodes/node-abc123/drain").mock(
            return_value=httpx.Response(
                200,
                json={
                    "node_id": "node-abc123",
                    "state": "NODE_STATE_DRAINING",
                    "message": "node cordoned",
                },
            )
        )
        result = client.drain_node("node-abc123")

    assert result is None  # method returns None


def test_delete_node_no_content(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.delete("/api/v1/nodes/node-abc123").mock(
            return_value=httpx.Response(204)
        )
        result = client.delete_node("node-abc123")

    assert result is None


def test_delete_node_conflict(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.delete("/api/v1/nodes/node-abc123").mock(
            return_value=httpx.Response(
                409,
                json={
                    "error": "node_in_use",
                    "message": "node still hosts active deployments",
                    "deployments": ["dep-1"],
                },
            )
        )
        with pytest.raises(ConflictError) as exc_info:
            client.delete_node("node-abc123")

    assert exc_info.value.status_code == 409


# ---------------------------------------------------------------------------
# Models
# ---------------------------------------------------------------------------


MODEL_PAYLOAD = {
    "id": "llama3-8b",
    "family": "llama",
    "architecture": "transformer",
    "params_total_b": 8.0,
    "engine": "llama.cpp",
    "created_at": "2024-01-16T08:00:00Z",
    "updated_at": "2024-01-16T08:00:00Z",
}


def test_list_models_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/models").mock(
            return_value=httpx.Response(200, json={"models": [MODEL_PAYLOAD]})
        )
        models = client.list_models()

    assert len(models) == 1
    m = models[0]
    assert isinstance(m, Model)
    assert m.id == "llama3-8b"
    assert m.family == "llama"
    assert m.params_total_b == 8.0
    assert m.engine == "llama.cpp"


def test_create_model_sends_correct_body(client: PurserClient) -> None:
    spec = ModelSpec(
        model_id="llama3-8b",
        family="llama",
        architecture="transformer",
        params_total_b=8.0,
        engine="llama.cpp",
    )
    captured: list[httpx.Request] = []

    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        def capture(request: httpx.Request) -> httpx.Response:
            captured.append(request)
            return httpx.Response(201, json={"model_id": "llama3-8b"})

        router.post("/api/v1/models").mock(side_effect=capture)
        # Also mock the follow-up list_models GET
        router.get("/api/v1/models").mock(
            return_value=httpx.Response(200, json={"models": [MODEL_PAYLOAD]})
        )
        model = client.create_model(spec)

    assert len(captured) == 1
    body = json.loads(captured[0].content)
    assert body["modelId"] == "llama3-8b"
    assert body["family"] == "llama"
    assert body["paramsTotalB"] == 8.0
    assert body["engine"] == "llama.cpp"
    assert model.id == "llama3-8b"


def test_create_model_conflict(client: PurserClient) -> None:
    spec = ModelSpec(model_id="llama3-8b")
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/models").mock(
            return_value=httpx.Response(
                409,
                json={"error": "model_exists", "message": "model already exists: llama3-8b"},
            )
        )
        with pytest.raises(ConflictError):
            client.create_model(spec)


def test_delete_model_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.delete("/api/v1/models/llama3-8b").mock(
            return_value=httpx.Response(204)
        )
        result = client.delete_model("llama3-8b")

    assert result is None


def test_preview_plan_feasible(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/models/llama3-8b/plan").mock(
            return_value=httpx.Response(
                200,
                json={
                    "feasible": True,
                    "id": "plan-001",
                    "model_id": "llama3-8b",
                    "quantization": "Q4_K_M",
                    "cost": 1.5,
                },
            )
        )
        preview = client.preview_plan("llama3-8b")

    assert isinstance(preview, PlanPreview)
    assert preview.feasible is True
    assert preview.plan is not None
    assert preview.plan.id == "plan-001"
    assert preview.reason == ""


def test_preview_plan_infeasible(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/models/llama3-70b/plan").mock(
            return_value=httpx.Response(
                200,
                json={"feasible": False, "reason": "insufficient VRAM: need 35 GB, have 24 GB"},
            )
        )
        preview = client.preview_plan("llama3-70b")

    assert preview.feasible is False
    assert preview.plan is None
    assert "VRAM" in preview.reason


def test_deploy_model_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/models/llama3-8b/deploy").mock(
            return_value=httpx.Response(
                202,
                json={
                    "deployment_id": "dep-xyz",
                    "model_id": "llama3-8b",
                    "plan_id": "plan-001",
                },
            )
        )
        dep = client.deploy_model("llama3-8b")

    assert isinstance(dep, Deployment)
    assert dep.id == "dep-xyz"
    assert dep.model_id == "llama3-8b"
    assert dep.state == "provisioning"


# ---------------------------------------------------------------------------
# Deployments
# ---------------------------------------------------------------------------


DEPLOYMENT_PAYLOAD = {
    "id": "dep-xyz",
    "model_id": "llama3-8b",
    "plan_id": "plan-001",
    "state": "DEPLOYMENT_STATE_ACTIVE",
    "created_at": "2024-01-16T09:00:00Z",
    "updated_at": "2024-01-16T09:01:00Z",
}


def test_list_deployments(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/deployments").mock(
            return_value=httpx.Response(
                200, json={"deployments": [DEPLOYMENT_PAYLOAD]}
            )
        )
        deps = client.list_deployments()

    assert len(deps) == 1
    assert deps[0].id == "dep-xyz"
    assert deps[0].state == "DEPLOYMENT_STATE_ACTIVE"


def test_delete_deployment_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.delete("/api/v1/deployments/dep-xyz").mock(
            return_value=httpx.Response(
                200, json={"deployment_id": "dep-xyz", "state": "stopping"}
            )
        )
        result = client.delete_deployment("dep-xyz")

    assert result is None


def test_get_plan(client: PurserClient) -> None:
    plan_payload = {
        "id": "plan-001",
        "model_id": "llama3-8b",
        "quantization": "Q4_K_M",
        "cost": 1.5,
        "created_at": "2024-01-16T09:00:00Z",
    }
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/plans/plan-001").mock(
            return_value=httpx.Response(200, json=plan_payload)
        )
        plan = client.get_plan("plan-001")

    assert isinstance(plan, Plan)
    assert plan.id == "plan-001"
    assert plan.quantization == "Q4_K_M"
    assert plan.cost == 1.5


# ---------------------------------------------------------------------------
# API keys
# ---------------------------------------------------------------------------


APIKEY_PAYLOAD = {
    "id": "key-deadbeef",
    "name": "dev-key",
    "tenant": "acme",
    "quota": 1000,
    "enabled": True,
    "created_at": "2024-01-17T10:00:00Z",
    "updated_at": "2024-01-17T10:00:00Z",
}


def test_list_api_keys(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/apikeys").mock(
            return_value=httpx.Response(200, json={"apikeys": [APIKEY_PAYLOAD]})
        )
        keys = client.list_api_keys()

    assert len(keys) == 1
    k = keys[0]
    assert isinstance(k, APIKey)
    assert k.id == "key-deadbeef"
    assert k.name == "dev-key"
    assert k.key == ""  # not included in list response


def test_create_api_key_returns_plaintext(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/apikeys").mock(
            return_value=httpx.Response(
                201,
                json={
                    "id": "key-newkey",
                    "name": "prod-key",
                    "tenant": "acme",
                    "key": "psk_AABBCC",
                },
            )
        )
        key = client.create_api_key("prod-key", tenant="acme")

    assert key.id == "key-newkey"
    assert key.key == "psk_AABBCC"


def test_delete_api_key_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.delete("/api/v1/apikeys/key-deadbeef").mock(
            return_value=httpx.Response(204)
        )
        result = client.delete_api_key("key-deadbeef")

    assert result is None


# ---------------------------------------------------------------------------
# Join token
# ---------------------------------------------------------------------------


def test_create_join_token(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/join-token").mock(
            return_value=httpx.Response(
                201,
                json={
                    "token": "tkn_abc123",
                    "expires_at": "2024-01-18T10:00:00Z",
                    "cluster_id": "prod-cluster",
                },
            )
        )
        tok = client.create_join_token(ttl_seconds=3600)

    assert isinstance(tok, JoinToken)
    assert tok.token == "tkn_abc123"
    assert tok.cluster_id == "prod-cluster"


# ---------------------------------------------------------------------------
# Cluster health
# ---------------------------------------------------------------------------


def test_cluster_health(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/cluster/health").mock(
            return_value=httpx.Response(
                200,
                json={
                    "status": "ok",
                    "total_nodes": 3,
                    "ready_nodes": 3,
                    "checked_at": "2024-01-17T12:00:00Z",
                },
            )
        )
        health = client.cluster_health()

    assert isinstance(health, ClusterHealth)
    assert health.status == "ok"
    assert health.total_nodes == 3
    assert health.ready_nodes == 3
    assert health.checked_at is not None


# ---------------------------------------------------------------------------
# Enterprise
# ---------------------------------------------------------------------------


def test_enterprise_status_community(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/enterprise/status").mock(
            return_value=httpx.Response(
                200,
                json={"edition": "community", "licensee": "community", "features": []},
            )
        )
        status = client.enterprise_status()

    assert isinstance(status, EnterpriseStatus)
    assert status.edition == "community"
    assert status.features == []


def test_audit_log_requires_license(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/enterprise/audit-log").mock(
            return_value=httpx.Response(
                402,
                json={
                    "error": {
                        "message": "enterprise license required",
                        "feature": "audit",
                        "type": "license_required",
                    }
                },
            )
        )
        with pytest.raises(LicenseRequiredError) as exc_info:
            client.audit_log()

    assert exc_info.value.status_code == 402
    assert exc_info.value.feature == "audit"


def test_audit_log_success(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/enterprise/audit-log").mock(
            return_value=httpx.Response(
                200,
                json={
                    "feature": "audit",
                    "licensee": "acme-corp",
                    "entries": [
                        {
                            "id": 1,
                            "actor": "api",
                            "action": "model.created",
                            "target": "llama3-8b",
                            "created_at": "2024-01-16T08:00:00Z",
                            "seq": 1,
                            "prev_hash": "",
                            "hash": "abc123",
                        }
                    ],
                    "chain": {"verified": True, "length": 1},
                },
            )
        )
        log = client.audit_log(limit=10)

    assert isinstance(log, AuditLog)
    assert log.licensee == "acme-corp"
    assert len(log.entries) == 1
    assert log.entries[0].action == "model.created"
    assert log.chain is not None
    assert log.chain.verified is True
    assert log.chain.length == 1


# ---------------------------------------------------------------------------
# Error handling
# ---------------------------------------------------------------------------


def test_404_raises_not_found_error(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes/ghost").mock(
            return_value=httpx.Response(
                404, json={"error": "not_found", "message": "node not found"}
            )
        )
        with pytest.raises(NotFoundError) as exc_info:
            client.get_node("ghost")

    err = exc_info.value
    assert isinstance(err, NotFoundError)
    assert isinstance(err, PurserError)
    assert err.status_code == 404


def test_409_raises_conflict_error(client: PurserClient) -> None:
    spec = ModelSpec(model_id="duplicate")
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.post("/api/v1/models").mock(
            return_value=httpx.Response(
                409,
                json={"error": "model_exists", "message": "model already exists: duplicate"},
            )
        )
        with pytest.raises(ConflictError) as exc_info:
            client.create_model(spec)

    assert exc_info.value.status_code == 409


def test_402_raises_license_required_error(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/enterprise/audit-log").mock(
            return_value=httpx.Response(
                402,
                json={
                    "error": {
                        "message": "enterprise license required",
                        "feature": "audit",
                        "type": "license_required",
                    }
                },
            )
        )
        with pytest.raises(LicenseRequiredError) as exc_info:
            client.audit_log()

    err = exc_info.value
    assert isinstance(err, LicenseRequiredError)
    assert isinstance(err, PurserError)
    assert err.status_code == 402
    assert err.feature == "audit"


def test_500_raises_purser_error(client: PurserClient) -> None:
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes").mock(
            return_value=httpx.Response(
                500,
                json={"error": "list_nodes_failed", "message": "database unavailable"},
            )
        )
        with pytest.raises(PurserError) as exc_info:
            client.list_nodes()

    err = exc_info.value
    assert err.status_code == 500
    assert not isinstance(err, NotFoundError)
    assert not isinstance(err, ConflictError)


# ---------------------------------------------------------------------------
# base_url normalisation
# ---------------------------------------------------------------------------


def test_trailing_slash_normalised() -> None:
    """A base_url with a trailing slash must be silently stripped."""
    c = PurserClient("http://purser.test/")
    assert c._base_url == "http://purser.test"
    c.close()


def test_trailing_slash_multiple() -> None:
    c = PurserClient("http://purser.test///")
    assert c._base_url == "http://purser.test"
    c.close()


# ---------------------------------------------------------------------------
# Authorization header
# ---------------------------------------------------------------------------


def test_api_key_sent_as_bearer(client_with_key: PurserClient) -> None:
    captured: list[httpx.Request] = []

    def capture(request: httpx.Request) -> httpx.Response:
        captured.append(request)
        return httpx.Response(200, json={"nodes": []})

    with respx.mock(base_url=BASE_URL, assert_all_called=False) as router:
        router.get("/api/v1/nodes").mock(side_effect=capture)
        client_with_key.list_nodes()

    assert len(captured) == 1
    assert captured[0].headers["authorization"] == "Bearer psk_testkey"


# ---------------------------------------------------------------------------
# Stubs raise NotImplementedError
# ---------------------------------------------------------------------------


def test_restart_node_not_implemented(client: PurserClient) -> None:
    with pytest.raises(NotImplementedError):
        client.restart_node("node-abc123")


def test_get_model_health_not_implemented(client: PurserClient) -> None:
    with pytest.raises(NotImplementedError):
        client.get_model_health("llama3-8b")


def test_get_key_usage_not_implemented(client: PurserClient) -> None:
    with pytest.raises(NotImplementedError):
        client.get_key_usage("key-deadbeef")


# ---------------------------------------------------------------------------
# Context manager
# ---------------------------------------------------------------------------


def test_context_manager() -> None:
    with PurserClient(BASE_URL) as c:
        assert isinstance(c, PurserClient)
    # After __exit__ the client is closed; no assertion needed beyond no crash.
