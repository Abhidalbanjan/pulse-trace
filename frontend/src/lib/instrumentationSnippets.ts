// Guided instrumentation snippets (Onboarding · E1).
//
// Pure templating: given a platform, the tenant's OTLP endpoint, and a freshly
// minted ingestion key, produce real OpenTelemetry setup snippets (install +
// run/config + a curl "send a test event" one-liner) — no fictional vendor
// agent. Kept as a pure module so the templating is deterministic and testable,
// and the Wizard just renders what it returns.

export type Platform =
  | 'Node.js'
  | 'Python'
  | 'Go'
  | 'Java'
  | 'Browser (RUM)'
  | 'Kubernetes'
  | 'Docker'
  | 'Migrate (Datadog/Splunk)';

export interface SnippetInputs {
  endpoint: string; // OTLP/HTTP base, e.g. https://app.pulsetrace.com
  apiKey: string;   // per-tenant ingestion key (Bearer)
  service?: string; // service.name to stamp (defaults per platform)
}

export interface InstrumentationSnippet {
  language: string;       // for syntax highlighting
  install: string;        // dependency / agent install
  installLabel: string;
  run: string;            // run/config with OTel env wired to the endpoint + key
  runLabel: string;
  test: string;           // curl one-liner that sends a real OTLP log
  testLabel: string;
}

export const PLATFORMS: Array<{ name: Platform; desc: string; icon: string }> = [
  { name: 'Node.js', desc: 'OTel auto-instrumentation', icon: 'terminal' },
  { name: 'Python', desc: 'opentelemetry-instrument', icon: 'code' },
  { name: 'Go', desc: 'OTel SDK + profiling', icon: 'memory' },
  { name: 'Java', desc: 'OTel Java agent', icon: 'coffee' },
  { name: 'Browser (RUM)', desc: 'Real-user monitoring', icon: 'language' },
  { name: 'Kubernetes', desc: 'OTel Collector DaemonSet', icon: 'deployed_code' },
  { name: 'Docker', desc: 'Container env wiring', icon: 'inventory_2' },
  { name: 'Migrate (Datadog/Splunk)', desc: 'Repoint existing agents', icon: 'sync_alt' },
];

// trimSlash normalizes the endpoint so joining paths never doubles a slash.
function trimSlash(u: string): string {
  return u.replace(/\/+$/, '');
}

// testCurl is the shared "send a test event" one-liner: a minimal but valid
// OTLP/HTTP JSON log posted with the Bearer ingestion key, so the user can prove
// connectivity from a shell before wiring their whole app.
function testCurl(endpoint: string, apiKey: string, service: string): string {
  const base = trimSlash(endpoint);
  return [
    `curl -X POST ${base}/v1/logs \\`,
    `  -H "Authorization: Bearer ${apiKey}" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name",`,
    `       "value":{"stringValue":"${service}"}}]},"scopeLogs":[{"logRecords":[`,
    `       {"body":{"stringValue":"hello from pulsetrace onboarding"},"severityText":"INFO"}]}]}]}'`,
  ].join('\n');
}

// otelEnv is the standard OTLP exporter env every SDK/agent honors, wired to the
// tenant's endpoint and key.
function otelEnv(endpoint: string, apiKey: string, service: string): string {
  const base = trimSlash(endpoint);
  return [
    `export OTEL_EXPORTER_OTLP_ENDPOINT="${base}"`,
    `export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer ${apiKey}"`,
    `export OTEL_SERVICE_NAME="${service}"`,
  ].join('\n');
}

// buildInstrumentationSnippet returns the real OTel setup for a platform. Pure.
export function buildInstrumentationSnippet(platform: Platform, inputs: SnippetInputs): InstrumentationSnippet {
  const base = trimSlash(inputs.endpoint);
  const svc = (inputs.service && inputs.service.trim()) || defaultService(platform);
  const test = testCurl(base, inputs.apiKey, svc);
  const env = otelEnv(base, inputs.apiKey, svc);

  switch (platform) {
    case 'Node.js':
      return {
        language: 'bash',
        installLabel: 'Install the OpenTelemetry SDK',
        install: 'npm install @opentelemetry/api @opentelemetry/auto-instrumentations-node @opentelemetry/exporter-trace-otlp-http',
        runLabel: 'Run your app with auto-instrumentation',
        run: `${env}\nnode --require @opentelemetry/auto-instrumentations-node/register app.js`,
        testLabel: 'Send a test event',
        test,
      };
    case 'Python':
      return {
        language: 'bash',
        installLabel: 'Install & bootstrap OpenTelemetry',
        install: 'pip install opentelemetry-distro opentelemetry-exporter-otlp\nopentelemetry-bootstrap -a install',
        runLabel: 'Run your app instrumented',
        run: `${env}\nopentelemetry-instrument python app.py`,
        testLabel: 'Send a test event',
        test,
      };
    case 'Go':
      return {
        language: 'bash',
        installLabel: 'Add the OpenTelemetry SDK',
        install: 'go get go.opentelemetry.io/otel \\\n  go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp \\\n  go.opentelemetry.io/otel/sdk',
        runLabel: 'Export to PulseTrace (SDK reads OTEL_* env)',
        run: `${env}\n# Initialize the OTLP HTTP exporter in main() using otlptracehttp.New(ctx),\n# which honors the OTEL_EXPORTER_OTLP_* env above. Then run:\ngo run .`,
        testLabel: 'Send a test event',
        test,
      };
    case 'Java':
      return {
        language: 'bash',
        installLabel: 'Download the OpenTelemetry Java agent',
        install: 'curl -L -o opentelemetry-javaagent.jar \\\n  https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/latest/download/opentelemetry-javaagent.jar',
        runLabel: 'Attach the agent and run',
        run: `${env}\njava -javaagent:./opentelemetry-javaagent.jar -jar myapp.jar`,
        testLabel: 'Send a test event',
        test,
      };
    case 'Browser (RUM)':
      return {
        language: 'html',
        installLabel: 'Add the RUM snippet to your <head>',
        install: `<script>\n  window.PT_RUM = { endpoint: "${base}", apiKey: "${inputs.apiKey}", service: "${svc}" };\n</script>\n<script async src="${base}/rum/v1/loader.js"></script>`,
        runLabel: 'That’s it — page views, web-vitals & JS errors stream automatically',
        run: '// No build step required. Reload your page to start sending\n// page_view, web_vitals and error events to PulseTrace RUM.',
        testLabel: 'Verify from the shell (optional)',
        test,
      };
    case 'Kubernetes':
      return {
        language: 'bash',
        installLabel: 'Install the OpenTelemetry Collector (DaemonSet)',
        install: 'helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts\nhelm repo update',
        runLabel: 'Point its OTLP exporter at PulseTrace',
        run: `helm install otel-collector open-telemetry/opentelemetry-collector \\\n  --set mode=daemonset \\\n  --set 'config.exporters.otlphttp.endpoint=${base}' \\\n  --set 'config.exporters.otlphttp.headers.Authorization=Bearer ${inputs.apiKey}' \\\n  --set 'config.service.pipelines.traces.exporters={otlphttp}'`,
        testLabel: 'Send a test event',
        test,
      };
    case 'Docker':
      return {
        language: 'bash',
        installLabel: 'Wire OTel env into your container',
        install: '# Any OTel-instrumented image honors the OTEL_EXPORTER_OTLP_* env below.',
        runLabel: 'Run with the exporter pointed at PulseTrace',
        run: `docker run -d \\\n  -e OTEL_EXPORTER_OTLP_ENDPOINT="${base}" \\\n  -e OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer ${inputs.apiKey}" \\\n  -e OTEL_SERVICE_NAME="${svc}" \\\n  my-app:latest`,
        testLabel: 'Send a test event',
        test,
      };
    case 'Migrate (Datadog/Splunk)':
      return {
        language: 'yaml',
        installLabel: 'Already running a Collector? Just add an exporter',
        install: '# No new agent needed — repoint your existing OTel Collector\n# (or Datadog Agent OTLP / Splunk OTel) at PulseTrace.',
        runLabel: 'Add PulseTrace as an otlphttp exporter',
        run: `exporters:\n  otlphttp/pulsetrace:\n    endpoint: ${base}\n    headers:\n      Authorization: "Bearer ${inputs.apiKey}"\nservice:\n  pipelines:\n    traces:  { exporters: [otlphttp/pulsetrace] }\n    metrics: { exporters: [otlphttp/pulsetrace] }\n    logs:    { exporters: [otlphttp/pulsetrace] }`,
        testLabel: 'Send a test event',
        test,
      };
  }
}

function defaultService(platform: Platform): string {
  switch (platform) {
    case 'Browser (RUM)':
      return 'web-frontend';
    case 'Node.js':
      return 'my-node-service';
    case 'Python':
      return 'my-python-service';
    case 'Go':
      return 'my-go-service';
    case 'Java':
      return 'my-java-service';
    default:
      return 'my-service';
  }
}
