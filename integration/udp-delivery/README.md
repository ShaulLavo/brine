# UDP delivery fixture checks

These tests start the fixture's real HTTP and UDP listeners on loopback and run the separate bounded client. They cover release identity, refusal, timeout and oversized nonce behavior without touching Brine lifecycle or host network policy.
