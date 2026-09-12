import { chromium } from "playwright";
import assert from "node:assert/strict";
import { createServer } from "vite";
import { mkdtemp, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
const artifacts =
  process.env.UI_SCREENSHOT_DIR || (await mkdtemp(join(tmpdir(), "talos-ui-")));
await mkdir(artifacts, { recursive: true });
const server = await createServer({
  server: { host: "127.0.0.1", port: 5175, strictPort: true },
});
await server.listen();
const browser = await chromium.launch({
  executablePath: process.env.CHROMIUM_PATH || "/usr/bin/chromium",
  headless: true,
  args: ["--no-sandbox"],
});
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1050 },
  });
  const errors = [];
  const mutations = [];
  await context.addInitScript(() =>
    localStorage.setItem("talosdeck_token", "fixture-token"),
  );
  const nodes = [
    {
      hostname: "talos-cp-01",
      ip: "10.42.0.110",
      role: "controlplane",
      ready: true,
      cpuUsage: 17.3,
      memoryUsage: "2.1 / 8 GiB",
      version: "v1.14.0",
      uptime: "14d 6h",
    },
    {
      hostname: "talos-worker-01",
      ip: "10.42.0.111",
      role: "worker",
      ready: true,
      cpuUsage: 42.6,
      memoryUsage: "5.8 / 16 GiB",
      version: "v1.14.0",
      uptime: "14d 6h",
    },
    {
      hostname: "talos-worker-02",
      ip: "10.42.0.112",
      role: "worker",
      ready: false,
      cpuUsage: 0,
      memoryUsage: "—",
      version: "v1.14.0",
      uptime: "—",
    },
  ];
  const fixtures = {
    "/api/auth/providers": { oidc: { enabled: false } },
    "/api/auth/users": {
      users: [
        {
          id: "admin-id",
          username: "admin",
          role: "admin",
          provider: "local",
          disabled: false,
        },
      ],
    },
    "/api/providers": [
      {
        id: "pve-provider",
        name: "Test Proxmox",
        kind: "proxmox",
        baseUrl: "https://pve.example:8006",
        node: "pve01",
        defaultStorage: "local-lvm",
        defaultISO: "local:iso/talos.iso",
        defaultBridge: "vmbr0",
      },
    ],
    "/api/provision/jobs": [],
    "/api/jobs": [],
    "/api/machines": [],
    "/api/k8s/workloads": {
      deployments: [],
      daemonsets: [],
      statefulsets: [],
      jobs: [],
      cronjobs: [],
    },
    "/api/k8s/events": { events: [] },
    "/api/k8s/storage": {
      persistentVolumes: [],
      persistentVolumeClaims: [],
      storageClasses: [],
    },
    "/api/backups/targets": {
      targets: [
        { id: "local", name: "Local", type: "local", configured: true },
      ],
    },
    "/api/backups/schedule": {
      enabled: false,
      intervalHours: 6,
      retention: 30,
      targetId: "local",
    },
    "/api/diagnostics": {
      status: "healthy",
      checkedAt: "2026-09-12T00:00:00Z",
      checks: [],
      summary: { critical: 0, warning: 0, info: 0 },
    },
    "/api/nodes": nodes,
    "/api/cluster": {
      name: "production-eu-01",
      endpoint: "https://10.42.0.110:6443",
      talosVersion: "v1.14.0",
      kubernetesVersion: "v1.32.2",
    },
    "/api/k8s/pods": [
      {
        id: "p1",
        name: "coredns-8c66d6799-2txbh",
        namespace: "kube-system",
        status: "Running",
        nodeName: "talos-worker-01",
        readyContainers: "1/1",
        restarts: 0,
        ip: "10.244.1.7",
        age: "14d",
      },
      {
        id: "p2",
        name: "api-gateway-74bdf96d-ltf89",
        namespace: "production",
        status: "CrashLoopBackOff",
        nodeName: "talos-worker-02",
        readyContainers: "0/1",
        restarts: 7,
        ip: "10.244.2.3",
        age: "6h",
      },
    ],
    "/api/cluster/etcd": {
      healthy: true,
      leaderName: "talos-cp-01",
      totalDbSize: "24 MiB",
      raftTerm: 4,
      raftIndex: 218441,
      members: [
        {
          id: "1",
          name: "talos-cp-01",
          healthy: true,
          leader: true,
          dbSize: "24 MiB",
          peerUrls: ["https://10.42.0.110:2380"],
        },
      ],
      alarms: [],
    },
    "/api/proxmox/status": {
      configured: true,
      node: "pve-01",
      status: {
        cpuUsagePercent: 22,
        memory: { available: 18000000000 },
        storage: { free: 380000000000 },
      },
    },
    "/api/auth/me": {
      authenticated: true,
      user: { username: "admin", role: "admin" },
    },
    "/api/auth/login": {
      token: "fixture-test-token",
      user: { username: "admin", role: "admin" },
    },
    "/api/proxmox/next-vmid": { vmid: 114 },
    "/api/backups": [
      {
        id: "b1",
        filename: "etcd-2026-09-11.snapshot",
        type: "etcd",
        humanSize: "24 MiB",
        timestamp: "2026-09-11T09:00:00Z",
        node: "talos-cp-01",
      },
    ],
    "/api/alerts/config": {
      enabled: true,
      bot_configured: true,
      chat_id: "-10012345678",
      min_level: "WARNING",
    },
    "/api/audit": [
      {
        id: "a1",
        action: "node.reboot",
        user: "admin",
        status: "success",
        ip: "192.168.88.10",
        timestamp: "2026-09-11T10:00:00Z",
        details: { node: "10.42.0.111" },
      },
    ],
  };
  await context.route("**/api/**", async (route) => {
    const req = route.request();
    const raw = new URL(req.url()).pathname;
    const p = raw.replace(/^\/api\/clusters\/[^/]+\//, "/api/");
    if (!p.startsWith("/api/")) return route.continue();
    if (req.method() !== "GET") mutations.push(p);
    let body = fixtures[p] || { success: true };
    if (p === "/api/clusters")
      body = { clusters: [{ id: "cluster-a", name: "production-eu-01" }] };
    if (p.endsWith("/history")) body = [];
    if (p === "/api/provision/plan")
      body = {
        id: "provision-plan",
        spec: req.postDataJSON(),
        safetyNotes: [],
      };
    if (p === "/api/provision" && req.method() === "POST")
      body = { id: "provision-job", status: "queued" };
    if (p.endsWith("/disks"))
      body = [
        {
          devicePath: "/dev/sda",
          model: "QEMU HARDDISK",
          prettySize: "100 GiB",
          size: 107374182400,
          type: "Virtual",
          bus: "SCSI",
          partitions: [
            {
              location: "/dev/sda1",
              id: "STATE",
              prettySize: "1 GiB",
              size: 1073741824,
              filesystem: "xfs",
              mountPath: "/system/state",
              used: "280 MiB",
            },
          ],
        },
      ];
    if (p.endsWith("/config") && p.includes("/nodes/"))
      body = {
        configYaml:
          "version: v1alpha1\nmachine:\n  type: controlplane\ncluster:\n  clusterName: production-eu-01\n---\napiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  environment: production",
      };
    if (p.endsWith("/services"))
      body = [
        {
          id: "kubelet",
          state: "Running",
          healthy: true,
          description: "Kubernetes node agent",
        },
      ];
    if (p.endsWith("/containers"))
      body = [
        {
          id: "container-1",
          name: "kubelet",
          status: "Running",
          image: "registry.k8s.io/kubelet:v1.32.2",
        },
      ];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  await context.routeWebSocket("**/ws/**", (ws) => {
    ws.send("kernel: test stream connected");
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto("http://127.0.0.1:5175/#overview");
  await page.getByRole("button", { name: "Открыть список нод" }).waitFor();
  await page.waitForTimeout(300);
  await page.screenshot({
    path: artifacts + "/console-overview.png",
    fullPage: true,
  });
  // Language changes update mounted sections, persist across reloads and keep
  // resource data intact. The project link is available in both languages.
  await page.getByLabel("Язык интерфейса", { exact: true }).selectOption("en");
  await page.getByRole("button", { name: "View nodes", exact: true }).waitFor();
  assert.equal(await page.locator("html").getAttribute("lang"), "en");
  assert.equal(
    await page
      .getByRole("link", { name: "Project on GitHub" })
      .getAttribute("href"),
    "https://github.com/etosheartem/TalosDeck",
  );
  await page.reload();
  await page.getByRole("button", { name: "View nodes", exact: true }).waitFor();
  assert.equal(await page.getByLabel("Interface language").inputValue(), "en");
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page
    .getByRole("button", { name: "Sign in", exact: true })
    .first()
    .click();
  await page
    .getByRole("dialog")
    .getByLabel("Password", { exact: true })
    .waitFor();
  await page.keyboard.press("Escape");
  await page.getByLabel("Interface language").selectOption("ru");
  await page
    .getByRole("button", { name: "Войти", exact: true })
    .first()
    .click();
  await page.getByLabel("Пароль", { exact: true }).fill("test");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Войти", exact: true })
    .click();
  await page.waitForTimeout(200);
  for (const id of [
    "nodes",
    "workloads",
    "storage",
    "config",
    "etcd",
    "backups",
    "maintenance",
    "audit",
    "settings",
    "alerts",
    "diagnostics",
    "events",
    "kube-storage",
    "machines",
    "providers",
    "users",
    "clusters",
  ]) {
    await page.goto(`http://127.0.0.1:5175/#${id}`);
    await page.waitForTimeout(220);
    assert.equal(await page.locator(".access-state").count(), 0);
    await page.screenshot({
      path: `${artifacts}/console-${id}.png`,
      fullPage: true,
    });
  }
  await page.getByLabel("Язык интерфейса", { exact: true }).selectOption("en");
  for (const [id, title] of Object.entries({
    overview: "Overview",
    nodes: "Nodes",
    workloads: "Workloads",
    storage: "Storage",
    config: "Configuration",
    etcd: "etcd",
    backups: "Backups",
    maintenance: "Maintenance",
    audit: "Audit",
    settings: "Connection",
    alerts: "Notifications",
    diagnostics: "Diagnostics",
    events: "Events",
    "kube-storage": "Volumes",
    machines: "Machines",
    providers: "Providers",
    users: "Access",
    clusters: "Clusters",
  })) {
    await page.goto(`http://127.0.0.1:5175/#${id}`);
    await page.getByRole("heading", { name: title, exact: true }).waitFor();
    await page.waitForTimeout(150);
    assert(
      !/[А-Яа-яЁё]/.test(await page.locator(".workspace").innerText()),
      `Russian UI text remains in ${id}`,
    );
  }
  await page.goto("http://127.0.0.1:5175/#nodes");
  await page.getByRole("button", { name: "talos-cp-01", exact: true }).click();
  await page.getByRole("button", { name: "Live logs", exact: true }).click();
  await page
    .getByText("kernel: test stream connected", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Pause", exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await page.getByLabel("Interface language").selectOption("ru");
  await page.goto("http://127.0.0.1:5175/#nodes");
  await page
    .getByRole("button", { name: "talos-cp-01", exact: true })
    .waitFor();
  await page.getByPlaceholder("Поиск по всем полям…").fill("worker-01");
  assert.equal(await page.locator("tbody tr").count(), 1);
  await page.getByPlaceholder("Поиск по всем полям…").fill("");
  await page.getByRole("button", { name: "talos-cp-01", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "kubelet", exact: true })
    .waitFor();
  await page.screenshot({
    path: artifacts + "/console-inspector.png",
    fullPage: true,
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Живые логи", exact: true })
    .click();
  await page
    .getByText("kernel: test stream connected", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Пауза", exact: true }).click();
  await page.getByRole("button", { name: "Продолжить", exact: true }).waitFor();
  await page.keyboard.press("Escape");
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page
    .getByRole("button", { name: "Добавить worker", exact: true })
    .click();
  await page.getByLabel("Имя", { exact: true }).fill("worker-test");
  await page.getByLabel("Источник образа",{exact:true}).selectOption("manual");
  await page.getByLabel("Talos", { exact: true }).fill("1.14.0");
  await page.getByLabel("Kubernetes", { exact: true }).fill("1.37.0");
  await page
    .getByLabel("Installer image", { exact: true })
    .fill("factory.talos.dev/installer/fixture:v1.14.0");
  await page
    .getByRole("button", { name: "Проверить план", exact: true })
    .click();
  await page
    .getByLabel("Введите имя кластера")
    .fill("production-eu-01");
  await page
    .getByRole("button", { name: "Создать через задание", exact: true })
    .click();
  await page.waitForTimeout(200);
  assert(mutations.includes("/api/provision"));
  await page.goto("http://127.0.0.1:5175/#maintenance");
  await page
    .getByRole("button", { name: "Перезагрузить", exact: true })
    .click();
  assert(!mutations.some((p) => p.endsWith("/reboot")));
  await page.getByRole("button", { name: "Отмена", exact: true }).click();
  assert(!mutations.some((p) => p.endsWith("/reboot")));
  await page.goto("http://127.0.0.1:5175/#alerts");
  await page.getByRole("button", {name:"Добавить канал",exact:true}).click();
  const channelDialog=page.getByRole('dialog');await channelDialog.getByLabel('Имя',{exact:true}).fill('Telegram');await channelDialog.getByLabel('Bot token',{exact:true}).fill('fixture-token');await channelDialog.getByLabel('Chat ID',{exact:true}).fill('123');
  await channelDialog.getByRole("button", { name: "Сохранить", exact: true }).click();
  await page.waitForTimeout(100);
  assert(mutations.includes("/api/notifications/channels"));
  await page.goto("http://127.0.0.1:5175/#storage");
  await page.getByRole("button", { name: "/dev/sda", exact: true }).click();
  await page
    .getByRole("cell", { name: "/system/state", exact: true })
    .waitFor();
  await page.keyboard.press("Escape");
  await page.goto("http://127.0.0.1:5175/#etcd");
  await page
    .getByRole("cell", { name: 'https://10.42.0.110:2380', exact: true })
    .waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  for (const id of ["overview", "nodes", "workloads", "storage", "settings"]) {
    await page.goto(`http://127.0.0.1:5175/#${id}`);
    await page.waitForTimeout(200);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    );
    assert(!overflow, `overflow ${id}`);
    await page.screenshot({
      path: `${artifacts}/console-mobile-${id}.png`,
      fullPage: true,
    });
  }
  await page.getByRole("button", { name: "Открыть меню" }).click();
  await page.getByRole("button", {name:"ОПЕРАЦИИ",exact:true}).click();
  await page.screenshot({path:`${artifacts}/console-mobile-sidebar.png`,fullPage:true});
  await page
    .getByRole("navigation")
    .getByRole("button", { name: "Аудит", exact: true })
    .click();
  assert.equal(new URL(page.url()).hash, "#audit");
  // Separate authenticated registry: identical node identifiers must not leak
  // data, confirmations, streams or configuration between cluster selections.
  const fleet = await browser.newContext({
    viewport: { width: 1440, height: 1050 },
  });
  await fleet.addInitScript(() =>
    localStorage.setItem("talosdeck_token", "fixture-token"),
  );
  const registry = [
    { id: "alpha", name: "Alpha" },
    { id: "beta", name: "Beta" },
  ];
  const fleetRequests = [];
  const jobs = [];
  let delayedAlpha = false;
  await fleet.route("**/api/**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (!path.startsWith("/api/")) return route.continue();
    fleetRequests.push({
      path,
      method: req.method(),
      body: req.postDataJSON(),
    });
    if (path.startsWith("/api/auth/"))
      return route.fulfill({ json: fixtures[path] });
    if (path === "/api/clusters") {
      if (req.method() === "POST") {
        assert(req.postDataJSON().talosconfig);
        assert(req.postDataJSON().kubeconfig);
        const cluster = { id: "gamma", name: req.postDataJSON().name };
        registry.push(cluster);
        return route.fulfill({ json: { cluster } });
      }
      return route.fulfill({ json: { clusters: registry } });
    }
    const match = path.match(/^\/api\/clusters\/([^/]+)(\/.*)$/);
    assert(match, `Unscoped infrastructure request: ${path}`);
    const [, id, suffix] = match;
    let body = fixtures[`/api${suffix}`] || {};
    if (suffix === "/nodes") {
      if (id === "alpha" && delayedAlpha)
        await new Promise((resolve) => setTimeout(resolve, 350));
      body = [
        { ...nodes[0], hostname: "same-node", memoryUsage: `${id}-memory` },
      ];
    }
    if (suffix === "/alerts") body={alerts:[{id:'same-alert',clusterId:id,ruleId:'node-not-ready',resourceId:'same-node',node:'same-node',component:'node',severity:'warning',state:'active',title:'Same alert',details:`${id}-alert-detail`,firstSeen:'2026-09-12T00:00:00Z',lastSeen:'2026-09-12T00:00:00Z',occurrences:1,observation:'known'}],summary:{active:1,critical:0,warning:1,silenced:0,stale:0},health:'degraded',lastCheckAt:'2026-09-12T00:00:00Z',total:1,nextOffset:null};
    if (suffix === "/certificates") body = {checkedAt:'2026-09-12T00:00:00Z',status:id==='alpha'?'healthy':'unknown',summary:{healthy:id==='alpha'?1:0,warning:0,critical:0,unknown:id==='alpha'?0:1},certificates:[{id:'same-cert',name:'Same API certificate',source:'talos-server',endpoint:`${id}.example:50000`,status:id==='alpha'?'healthy':'unknown',daysRemaining:id==='alpha'?365:undefined,reason:id==='alpha'?'valid':'unavailable',verification:id==='alpha'?'verified':'unavailable',verified:id==='alpha',renewalGuidance:'Inspect endpoint'}]};
    if (suffix === "/cluster") body = { ...fixtures["/api/cluster"], name: id };
    if (suffix.endsWith("/config"))
      body = {
        configYaml: `machine:\n  token: REDACTED\n---\napiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  environment: ${id}`,
      };
    if (suffix.endsWith("/history"))
      body = [
        {
          id: "revision-1",
          author: "admin",
          mode: "auto",
          status: "succeeded",
          createdAt: "2026-09-12T00:00:00Z",
        },
      ];
    if (suffix.endsWith("/history/revision-1"))
      body = {
        id: "revision-1",
        diff: "- old\n+ restored",
        config: "machine: REDACTED",
      };
    if (
      (suffix.endsWith("/plan") || suffix.endsWith("/restore-plan")) &&
      suffix.startsWith("/config/")
    )
      body = {
        id: "plan-1",
        diff: "- environment: alpha\n+ environment: updated",
        warnings: [],
        mode: "auto",
      };
    if (suffix.endsWith("/apply") || suffix.endsWith("/restore")) {
      const job = {
        id: `${id}-job`,
        clusterId: id,
        request: { kind: "config-apply" },
        status: "queued",
        user: "admin",
        createdAt: "2026-09-12T00:00:00Z",
        events: [],
      };
      jobs.push(job);
      body = job;
    }
    if (suffix === "/jobs") body = jobs.filter((job) => job.clusterId === id);
    if (suffix.startsWith("/jobs/"))
      body = jobs.find((job) => suffix === `/jobs/${job.id}`) || {};
    return route.fulfill({ json: body }).catch(() => {});
  });
  const fleetPage = await fleet.newPage();
  fleetPage.on("pageerror", (e) => errors.push(String(e)));
  await fleetPage.goto("http://127.0.0.1:5175/#nodes");
  await fleetPage
    .getByRole("cell", { name: "alpha-memory", exact: true })
    .waitFor();
  delayedAlpha = true;
  await fleetPage
    .getByRole("button", { name: "Обновить", exact: true })
    .click();
  await fleetPage.getByLabel("Кластер", { exact: true }).selectOption("beta");
  await fleetPage
    .getByRole("cell", { name: "beta-memory", exact: true })
    .waitFor();
  await fleetPage.waitForTimeout(450);
  assert.equal(
    await fleetPage.getByText("alpha-memory", { exact: true }).count(),
    0,
  );
  await fleetPage.reload();
  await fleetPage.waitForFunction(()=>document.querySelector('.cluster-picker select')?.value==='beta');
  assert.equal(
    await fleetPage.getByLabel("Кластер", { exact: true }).inputValue(),
    "beta",
  );
  await fleetPage.goto("http://127.0.0.1:5175/#config");
  await fleetPage
    .getByLabel("YAML patch", { exact: true })
    .fill(
      "apiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  environment: updated",
    );
  await fleetPage
    .getByRole("button", { name: "Проверить и сравнить", exact: true })
    .click();
  await fleetPage
    .getByRole("heading", { name: "Проверка изменений", exact: true })
    .waitFor();
  await fleetPage.getByLabel("Адрес ноды", { exact: true }).fill(nodes[0].ip);
  await fleetPage
    .getByRole("button", { name: "Применить через задание", exact: true })
    .click();
  await fleetPage
    .getByRole("heading", { name: "Задания", exact: true })
    .waitFor();
  assert(
    fleetRequests.some(
      (r) =>
        r.path === `/api/clusters/beta/config/${nodes[0].ip}/apply` &&
        r.body.confirmedNode === nodes[0].ip,
    ),
  );
  await fleetPage.goto("http://127.0.0.1:5175/#config");
  await fleetPage
    .getByRole("button", { name: "Просмотреть", exact: true })
    .click();
  await fleetPage
    .getByRole("button", { name: "Проверить восстановление", exact: true })
    .click();
  await fleetPage
    .getByRole("heading", { name: "Проверка изменений", exact: true })
    .waitFor();
  assert(!fleetRequests.some((r) => r.path.endsWith("/restore")));
  await fleetPage.getByLabel("Адрес ноды", { exact: true }).fill(nodes[0].ip);
  await fleetPage
    .getByRole("button", { name: "Восстановить через задание", exact: true })
    .click();
  await fleetPage
    .getByRole("heading", { name: "Задания", exact: true })
    .waitFor();
  assert(
    fleetRequests.some(
      (r) => r.path.endsWith("/restore") && r.body.planId === "plan-1",
    ),
  );
  await fleetPage.getByLabel("Кластер", { exact: true }).selectOption("alpha");
  await fleetPage.waitForTimeout(200);
  assert.equal(
    await fleetPage.getByText("beta-job", { exact: true }).count(),
    0,
  );
  await fleetPage.evaluate(()=>location.hash='alert-center');await fleetPage.getByRole('button',{name:'Same alert',exact:true}).click();await fleetPage.getByText('alpha-alert-detail',{exact:true}).waitFor();await fleetPage.keyboard.press('Escape');await fleetPage.getByLabel('Кластер',{exact:true}).selectOption('beta');await fleetPage.getByRole('button',{name:'Same alert',exact:true}).click();await fleetPage.getByText('beta-alert-detail',{exact:true}).waitFor();assert.equal(await fleetPage.getByText('alpha-alert-detail',{exact:true}).count(),0);await fleetPage.keyboard.press('Escape');await fleetPage.getByLabel('Кластер',{exact:true}).selectOption('alpha');
  await fleetPage.evaluate(()=>location.hash='certificates');
  await fleetPage.getByRole('cell',{name:'alpha.example:50000',exact:true}).waitFor();
  await fleetPage.getByLabel('Кластер',{exact:true}).selectOption('beta');
  await fleetPage.getByRole('cell',{name:'beta.example:50000',exact:true}).waitFor();
  assert.equal(await fleetPage.getByText('alpha.example:50000',{exact:true}).count(),0);
  await fleetPage.getByRole('button',{name:'Same API certificate',exact:true}).click();
  await fleetPage.getByRole('dialog').getByText('Неизвестно',{exact:true}).waitFor();
  await fleetPage.keyboard.press('Escape');
  await fleetPage.getByLabel('Кластер',{exact:true}).selectOption('alpha');
  await fleetPage
    .getByRole("button", { name: "Добавить кластер", exact: true })
    .click();
  const importDialog = fleetPage.getByRole("dialog");
  await importDialog.getByLabel("Имя", { exact: true }).fill("Gamma");
  await importDialog
    .getByLabel("talosconfig", { exact: true })
    .fill("fixture-private-talos");
  await importDialog
    .getByLabel("kubeconfig", { exact: true })
    .fill("fixture-private-kube");
  await importDialog
    .getByRole("button", { name: "Подключить", exact: true })
    .click();
  await fleetPage.waitForFunction(
    () => document.querySelector(".cluster-picker select")?.value === "gamma",
  );
  assert.equal(await fleetPage.getByRole("dialog").count(), 0);
  assert(
    !(await fleetPage.evaluate(() => JSON.stringify(localStorage))).includes(
      "fixture-private",
    ),
  );
  await fleetPage
    .getByRole("button", { name: "Добавить кластер", exact: true })
    .click();
  assert.equal(
    await fleetPage.getByLabel("talosconfig", { exact: true }).inputValue(),
    "",
  );
  await fleetPage.keyboard.press("Escape");
  await fleet.close();
  const offline = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  await offline.route("**/api/**", (route) =>
    !new URL(route.request().url()).pathname.startsWith("/api/")
      ? route.continue()
      : route.fulfill({
          status: 503,
          body: JSON.stringify({ error: "Offline fixture" }),
          contentType: "application/json",
        }),
  );
  await offline.goto("http://127.0.0.1:5175");
  await offline
    .getByRole("heading", { name: "Кластеры", exact: true })
    .waitFor();
  assert.equal(
    await offline.getByText("talos-cp-01", { exact: true }).count(),
    0,
  );
  await offline.screenshot({
    path: artifacts + "/console-offline.png",
    fullPage: true,
  });
  await offline.goto("http://127.0.0.1:5175/#config");
  await offline
    .getByRole("heading", { name: "Кластеры", exact: true })
    .waitFor();
  assert.equal(await offline.locator(".code-lines").count(), 0);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: RU/EN, auth, console workflows, mobile layout, multi-cluster isolation with delayed responses and identical nodes, persisted selection, config preview/apply/restore jobs, import without browser credential persistence, offline/no mock fallback.",
  );
  console.log("Screenshots:", artifacts);
} finally {
  await browser.close();
  await server.close();
}
