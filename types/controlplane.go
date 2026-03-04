package types

const (
	// ControlPlaneCertCNPrefix is the CN prefix used for lease-bound client identity certs.
	ControlPlaneCertCNPrefix = "lease:"
	// ControlPlaneLeaseURIPrefix is the URI prefix used in lease-bound SPIFFE-like identities.
	ControlPlaneLeaseURIPrefix = "spiffe://portal/lease/"
)
