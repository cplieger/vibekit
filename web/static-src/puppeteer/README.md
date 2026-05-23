# Puppeteer smoke harness

Scaffolding for running ad-hoc browser-driven smoke / repro tests
against a real vibekit + Chromium. Not part of the regular test
suite — `*.cjs` test files in this directory are gitignored and
exist only when someone is actively diagnosing something.

## One-time setup

```sh
# from /workspace/vibekit
cd web/static-src
npm install --no-save puppeteer typescript

# OS deps for the bundled chromium (Debian trixie)
apt-get install -y \
    libglib2.0-0 libnss3 libnspr4 libdbus-1-3 libatk1.0-0 \
    libatk-bridge2.0-0 libcups2 libgtk-3-0 libgbm1 libxss1 \
    libxcomposite1 libxdamage1 libxrandr2 libxkbcommon0 \
    libpango-1.0-0 libcairo2 fonts-liberation libdrm2 \
    libxshmfence1 libatspi2.0-0 libasound2t64
```

## Build the bundle the browser will load

```sh
# from /workspace/vibekit
cd web/static-src
node_modules/.bin/tsc --project tsconfig.json
# Concatenate the CSS bundle (mirrors what the Dockerfile does).
cd ..
> static/style.css
while IFS= read -r line; do
    case "$line" in ''|\#*) continue ;; esac
    cat "static-src/css/${line}" >> static/style.css
done < static-src/css/MANIFEST
go build -o /tmp/vibekit .
```

## Write a test

```js
// /workspace/vibekit/web/static-src/puppeteer/my_test.cjs
const puppeteer = require('../node_modules/puppeteer');
const { spawn } = require('child_process');
const PORT = 19801;

const vibekit = spawn('/tmp/vibekit', [], {
  env: {
    ...process.env,
    // Set the env vars vibekit needs (see composition.ConfigFromEnv).
    // KWEB_ADDR or whatever the listen-addr env is for vibekit's
    // composition.ConfigFromEnv.
  },
  stdio: ['ignore', 'pipe', 'pipe'],
});
vibekit.stderr.pipe(process.stderr);

(async () => {
  // wait for vibekit on PORT, then puppeteer.launch(),
  // page.goto('http://127.0.0.1:' + PORT + '/'), drive the page...
  vibekit.kill('SIGTERM');
})();
```

Run with `node web/static-src/puppeteer/my_test.cjs`.

## Notes

- vibekit hosts an ACP-based chat UI for kiro-cli; spinning up a real
  kiro-cli inside the browser flow is slow (kiro-cli boot is 10-15s
  in headless), so for UI-shape tests it's usually faster to mock
  out the ACP backend or pre-warm via the Go test helpers.
- The chat UI uses xterm.js for terminal panels and a custom
  markdown renderer for messages — both have unit-test coverage in
  the regular vitest suite (`*.test.ts`); puppeteer is best for
  end-to-end flows that the unit tests can't cover (drag-and-drop,
  paste, focus handling, IME, viewport resize, reconnect).
