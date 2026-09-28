import collections

import grpc


class _ClientCallDetails(
    collections.namedtuple(
        "_ClientCallDetails",
        (
            "method",
            "timeout",
            "metadata",
            "credentials",
            "wait_for_ready",
            "compression",
        ),
    ),
    grpc.ClientCallDetails,
):
    pass


class TenantClientInterceptor(grpc.UnaryUnaryClientInterceptor):
    def __init__(self, tenant: str):
        self._tenant = tenant

    def intercept_unary_unary(self, continuation, client_call_details, request):
        metadata = [
            (key, value)
            for key, value in (client_call_details.metadata or [])
            if key != "x-tenant-id"
        ]
        metadata.append(("x-tenant-id", self._tenant))
        details = _ClientCallDetails(
            client_call_details.method,
            client_call_details.timeout,
            metadata,
            client_call_details.credentials,
            client_call_details.wait_for_ready,
            client_call_details.compression,
        )
        return continuation(details, request)
