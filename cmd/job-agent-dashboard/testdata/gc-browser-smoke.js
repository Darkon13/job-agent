// Run with the shared Playwright browser. Assets are fulfilled from the repo;
// every API request is mocked, so no server or live HH/backend is needed.
async (page) => {
  const testPage = await page.context().newPage();
  const errors = [];
  let bulkReads = 0, removals = 0;
  let pendingRead = false;
  const now = "2026-09-11T10:00:00Z";
  const application = (id, status = "submitted") => ({id, profile_id: "primary", platform: "hh", status, vacancy_title: "Backend Go", employer: "Сбер", updated_at: now, disposition: "pending"});
  const chat = {id: "chat-1", platform: "hh", profile_id: "primary", employer: "Сбер", vacancy_title: "Backend Go", unread_count: 2, status: "active", revision: 1, updated_at: now};
  const assert = (value, message) => { if (!value) throw Error(message); };
  testPage.on("pageerror", error => errors.push(error.message));
  await testPage.route("http://127.0.0.1:18088/**", async route => {
    const path = route.request().url().replace(/^https?:\/\/[^/]+/, "").split("?")[0];
    if (path.startsWith("/api/")) return route.fallback();
    const asset = {"/":"index.html", "/app.js":"app.js", "/styles.css":"styles.css"}[path];
    if (!asset) return route.fulfill({status:404, body:""});
    return route.fulfill({path:"/home/user/Projects/job-agent/cmd/job-agent-dashboard/web/"+asset});
  });
  await testPage.route("**/api/**", async route => {
    const request = route.request();
    const raw = request.url().replace(/^https?:\/\/[^/]+/, "");
    const url = {pathname:raw.split("?")[0], searchParams: new Map((raw.split("?")[1] || "").split("&").map(part => part.split("=").map(decodeURIComponent)))};
    let body = {items: []};
    if (url.pathname === "/api/v1/version") body = {version: "smoke", api_version: "v1"};
    else if (url.pathname === "/api/v1/profile-state/resources") body = [];
    else if (url.pathname === "/api/v1/dashboard/summary") body = {generated_at: now, conversations: [chat], tasks: pendingRead ? [{type: "conversation.mark_read", status: "new", count: 1}] : [], activity_snapshots: [{profile_id: "primary", platform: "hh", resume_id: "test-resume", score_hidden: true, observed_at: now}]};
    else if (url.pathname === "/api/v1/applications") {
      const query = url.searchParams.get("q");
      const offset = Number(url.searchParams.get("offset"));
      body = {items: query ? [application("older-match")] : offset ? [application("page-2")] : [application("page-1"), application("active", "submitting")], total: query ? 1 : 201, groups: query ? {"":1, waiting_invitation:1} : {"":201, waiting_invitation:200, queued:1}};
    } else if (url.pathname === "/api/v1/jobs") body = {items: [{tag:"gc-test", task_type:"application.retention", platform:"hh", profile_id:"primary", priority:0}]};
    else if (url.pathname === "/api/v1/applications/remove") {
      removals++;
      assert(JSON.stringify(request.postDataJSON().application_ids) === JSON.stringify(["older-match"]), "wrong selected object");
      body = {created: 1, tasks: [{task_id:"remove-1"}], results: [{application_id:"older-match", task_id:"remove-1"}]};
    } else if (url.pathname === "/api/v1/conversations/mark-read") {
      bulkReads++; pendingRead = true; body = {created:1};
    }
    await route.fulfill({status: 200, contentType:"application/json", body:JSON.stringify(body)});
  });
  try {
    await testPage.goto("http://127.0.0.1:18088/");
    await testPage.locator("#application-filter-state").filter({hasText:"из 201"}).waitFor();
    assert(await testPage.locator("#application-items input:disabled").count() === 1, "active application selectable");
    const layouts = [];
    for (const width of [1600, 1280, 900, 390]) {
      await testPage.setViewportSize({width, height: 1000});
      const geometry = await testPage.evaluate(() => ({width: innerWidth, document: document.documentElement.scrollWidth, body: document.body.scrollWidth}));
      assert(geometry.document <= width && geometry.body <= width, "page overflow " + JSON.stringify(geometry));
      layouts.push(geometry);
    }
    await testPage.setViewportSize({width:1280, height:1000});
    await testPage.locator("#application-next").click();
    await testPage.locator("#application-filter-state").filter({hasText:"201–201"}).waitFor();
    await testPage.locator("#application-prev").click();
    await testPage.locator("#application-search").fill("СБЕР");
    await testPage.locator("#application-filter-state").filter({hasText:"1–1 из 1"}).waitFor();
    await testPage.locator("#application-select-all").check();
    await testPage.locator("#application-bulk-action").selectOption("remove");
    await testPage.locator("#application-run-action").click();
    await testPage.locator("#application-selection-state").filter({hasText:"Создано задач: 1"}).waitFor();
    await testPage.locator("#mark-all-read").click();
    await testPage.locator("#connection-state").filter({hasText:"Backend доступен"}).waitFor();
    assert(await testPage.locator("#mark-all-read").isDisabled(), "bulk read not locked while tasks pending");
    assert(bulkReads === 1 && removals === 1, "duplicate mutation request");
    assert(errors.length === 0, errors.join("; "));
    await testPage.evaluate(() => window.scrollTo(0, 0));
    await testPage.screenshot({path:"/tmp/job-agent-dashboard-gc-smoke.png", fullPage:true});
    return {layouts, bulkReads, removals, pageErrors:errors};
  } finally { await testPage.close(); }
}
