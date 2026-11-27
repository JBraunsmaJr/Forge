package agent

// jobTokenEnvVar is the environment variable in which the scheduler places a
// job's own API token. The agent passes it through to the job's container and
// never substitutes its own token for it.
const jobTokenEnvVar = "FORGE_API_TOKEN"
