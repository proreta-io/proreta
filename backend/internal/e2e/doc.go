// Package e2e holds the end-to-end integration test: api, worker and jobs run in one test
// binary against a real Postgres and loopback TLS servers, with checks and Slack deliveries
// going through the real SSRF guard. The test is built only with `-tags proretatest`
// (`make test-e2e`), which is the only way to reach safehttp.NewLoopbackTestClient;
// it is skipped without TEST_DATABASE_ADMIN_URL.
package e2e
