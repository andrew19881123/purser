//! Bind-address safety for the unsandboxed `rpc-server` compute worker.
//!
//! `rpc-server` executes arbitrary compute graphs it receives over the wire and
//! is **not** sandboxed. If it were reachable from an untrusted network, anyone
//! able to connect could drive the accelerator. The adapter therefore refuses to
//! start a worker unless its bind address is:
//!
//! 1. a concrete IP (never a hostname we cannot reason about),
//! 2. **not** a wildcard (`0.0.0.0` / `::`, which would expose it on every
//!    interface, including public ones), and
//! 3. inside one of the configured *trusted subnets* (private/loopback by
//!    default).
//!
//! This is a defence-in-depth gate performed *before* any process is launched.
//! Operators who genuinely need a wider range must opt in explicitly via
//! [`crate::config::LlamaCppConfig::trusted_subnets`] /
//! `PURSER_LLAMACPP_TRUSTED_SUBNETS`; the wildcard is never accepted.

use std::net::{IpAddr, SocketAddr};

use purser_engine_adapter::EngineError;

/// Default CIDRs the `rpc-server` worker may bind to: loopback plus the RFC 1918
/// / RFC 4193 private ranges and link-local. Deliberately excludes any public
/// address.
pub const DEFAULT_TRUSTED_SUBNETS: &[&str] = &[
    "127.0.0.0/8",    // IPv4 loopback
    "10.0.0.0/8",     // private
    "172.16.0.0/12",  // private
    "192.168.0.0/16", // private
    "169.254.0.0/16", // IPv4 link-local
    "::1/128",        // IPv6 loopback
    "fc00::/7",       // IPv6 unique-local
    "fe80::/10",      // IPv6 link-local
];

/// A parsed CIDR block able to test IP membership. Family-aware: an IPv4 address
/// is never considered inside an IPv6 block and vice versa.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Cidr {
    base: IpAddr,
    prefix: u8,
}

impl Cidr {
    /// Parse `"10.0.0.0/8"`, `"::1/128"`, or a bare IP (treated as a host route:
    /// `/32` for IPv4, `/128` for IPv6).
    pub fn parse(s: &str) -> Result<Self, String> {
        let s = s.trim();
        let (ip_part, prefix_part) = match s.split_once('/') {
            Some((ip, pfx)) => (ip, Some(pfx)),
            None => (s, None),
        };
        let base: IpAddr = ip_part
            .parse()
            .map_err(|_| format!("invalid IP in CIDR {s:?}"))?;
        let max = match base {
            IpAddr::V4(_) => 32,
            IpAddr::V6(_) => 128,
        };
        let prefix = match prefix_part {
            Some(p) => p
                .parse::<u8>()
                .map_err(|_| format!("invalid prefix in CIDR {s:?}"))?,
            None => max,
        };
        if prefix > max {
            return Err(format!("prefix /{prefix} out of range for {s:?}"));
        }
        Ok(Self { base, prefix })
    }

    /// Whether `ip` falls inside this block.
    pub fn contains(&self, ip: &IpAddr) -> bool {
        match (self.base, ip) {
            (IpAddr::V4(base), IpAddr::V4(ip)) => {
                let b = u32::from_be_bytes(base.octets());
                let i = u32::from_be_bytes(ip.octets());
                let mask = mask_u32(self.prefix);
                (b & mask) == (i & mask)
            }
            (IpAddr::V6(base), IpAddr::V6(ip)) => {
                let b = u128::from_be_bytes(base.octets());
                let i = u128::from_be_bytes(ip.octets());
                let mask = mask_u128(self.prefix);
                (b & mask) == (i & mask)
            }
            // Different families never overlap.
            _ => false,
        }
    }
}

fn mask_u32(prefix: u8) -> u32 {
    if prefix == 0 {
        0
    } else if prefix >= 32 {
        u32::MAX
    } else {
        u32::MAX << (32 - prefix)
    }
}

fn mask_u128(prefix: u8) -> u128 {
    if prefix == 0 {
        0
    } else if prefix >= 128 {
        u128::MAX
    } else {
        u128::MAX << (128 - prefix)
    }
}

/// The built-in trusted subnets, parsed. Panics only if the compile-time
/// constants above are malformed (a bug), so it is safe to call.
pub fn default_trusted_subnets() -> Vec<Cidr> {
    DEFAULT_TRUSTED_SUBNETS
        .iter()
        .map(|s| Cidr::parse(s).expect("built-in CIDR must parse"))
        .collect()
}

/// Parse a comma-separated list of CIDRs (used for the env override). Empty
/// entries are ignored; any malformed entry aborts the whole parse.
pub fn parse_subnets(list: &str) -> Result<Vec<Cidr>, String> {
    list.split(',')
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(Cidr::parse)
        .collect()
}

/// Validate a worker `bind_addr` (`"ip:port"`) against the security policy,
/// returning the parsed [`SocketAddr`] on success.
///
/// Fails with [`EngineError::InvalidArgument`] if the address is not a literal
/// `ip:port`, is a wildcard, or is outside every trusted subnet.
pub fn validate_worker_bind(bind_addr: &str, trusted: &[Cidr]) -> Result<SocketAddr, EngineError> {
    let addr: SocketAddr = bind_addr.parse().map_err(|_| {
        EngineError::InvalidArgument(format!(
            "worker bind_addr {bind_addr:?} must be a literal ip:port (hostnames are not allowed \
             for the unsandboxed rpc-server)"
        ))
    })?;

    let ip = addr.ip();
    if ip.is_unspecified() {
        return Err(EngineError::InvalidArgument(format!(
            "rpc-server must never bind to the wildcard address ({ip}); it is unsandboxed and \
             would be exposed on every interface. Bind it to a concrete address inside the \
             trusted subnet."
        )));
    }

    if !trusted.iter().any(|c| c.contains(&ip)) {
        return Err(EngineError::InvalidArgument(format!(
            "rpc-server bind ip {ip} is not inside any trusted subnet; refusing to expose the \
             unsandboxed compute worker. Add the subnet to PURSER_LLAMACPP_TRUSTED_SUBNETS to \
             opt in."
        )));
    }

    Ok(addr)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cidr_ipv4_membership() {
        let c = Cidr::parse("10.0.0.0/8").unwrap();
        assert!(c.contains(&"10.1.2.3".parse().unwrap()));
        assert!(!c.contains(&"11.0.0.1".parse().unwrap()));
        // An IPv6 address is never inside an IPv4 block.
        assert!(!c.contains(&"::1".parse().unwrap()));
    }

    #[test]
    fn cidr_host_route_and_edges() {
        let host = Cidr::parse("192.168.1.5").unwrap();
        assert!(host.contains(&"192.168.1.5".parse().unwrap()));
        assert!(!host.contains(&"192.168.1.6".parse().unwrap()));

        let all = Cidr::parse("0.0.0.0/0").unwrap();
        assert!(all.contains(&"8.8.8.8".parse().unwrap()));
    }

    #[test]
    fn cidr_ipv6_membership() {
        let c = Cidr::parse("fc00::/7").unwrap();
        assert!(c.contains(&"fd12:3456::1".parse().unwrap()));
        assert!(!c.contains(&"2001:4860::1".parse().unwrap()));
    }

    #[test]
    fn cidr_rejects_bad_prefix() {
        assert!(Cidr::parse("10.0.0.0/33").is_err());
        assert!(Cidr::parse("::1/129").is_err());
        assert!(Cidr::parse("not-an-ip/8").is_err());
    }

    #[test]
    fn wildcard_is_rejected() {
        let trusted = default_trusted_subnets();
        let err = validate_worker_bind("0.0.0.0:7000", &trusted).unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
        let err6 = validate_worker_bind("[::]:7000", &trusted).unwrap_err();
        assert!(matches!(err6, EngineError::InvalidArgument(_)));
    }

    #[test]
    fn public_ip_is_rejected_by_default() {
        let trusted = default_trusted_subnets();
        let err = validate_worker_bind("8.8.8.8:7000", &trusted).unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[test]
    fn loopback_and_private_accepted() {
        let trusted = default_trusted_subnets();
        assert!(validate_worker_bind("127.0.0.1:7000", &trusted).is_ok());
        assert!(validate_worker_bind("192.168.10.20:7000", &trusted).is_ok());
        assert!(validate_worker_bind("10.5.6.7:7000", &trusted).is_ok());
    }

    #[test]
    fn hostname_is_rejected() {
        let trusted = default_trusted_subnets();
        let err = validate_worker_bind("localhost:7000", &trusted).unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[test]
    fn parse_subnets_roundtrip() {
        let v = parse_subnets("10.0.0.0/8, 192.168.0.0/16 ,").unwrap();
        assert_eq!(v.len(), 2);
        assert!(parse_subnets("10.0.0.0/8, garbage").is_err());
    }
}
