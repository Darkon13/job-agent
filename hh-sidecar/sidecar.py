import json, http.cookiejar, requests, time, sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN_FILE = "/home/darkon/.config/job-agent/hh-token.json"
COOKIE_FILE = "/home/darkon/.config/job-agent/hh-cookies.txt"
UA = "ru.hh.android/7.144.13722, Device: C5PKOM6TX3, Android OS: 11 (UUID: 6e968ac9-3103-40a9-a3b8-fbca16aa3c97)"

def load_token():
    return json.load(open(TOKEN_FILE))

def save_token(data):
    json.dump(data, open(TOKEN_FILE, "w"))

def session():
    s = requests.Session()
    cj = http.cookiejar.MozillaCookieJar(COOKIE_FILE)
    try: cj.load(ignore_discard=True, ignore_expires=True)
    except Exception: pass
    s.cookies.update(cj)
    return s

S = session()

def headers():
    return {"Authorization": "Bearer " + load_token()["access_token"], "Accept": "application/json",
            "X-Force-App-Access": "true", "X-HH-App-Active": "true", "User-Agent": UA}

def warm():
    for path in ("/me", "/resumes/mine", "/vacancies?per_page=1", "/employers/2180"):
        try: S.get("https://api.hh.ru" + path, headers=headers(), timeout=20)
        except Exception: pass
        time.sleep(0.6)

def remember():
    cj = http.cookiejar.MozillaCookieJar(COOKIE_FILE)
    for c in S.cookies:
        cj.set_cookie(c)
    try: cj.save(ignore_discard=True, ignore_expires=True)
    except Exception: pass

class H(BaseHTTPRequestHandler):
    def _json(self, code, data):
        body = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); req = json.loads(self.rfile.read(n) or b"{}")
        if self.path == "/apply":
            warm()
            r = S.post("https://api.hh.ru/negotiations", headers=headers(),
                       data={"vacancy_id": req.get("vacancy_id"), "resume_id": req.get("resume_id"), "message": req.get("message", "")}, timeout=30)
            remember()
            try: data = r.json()
            except Exception: data = {}
            self._json(r.status_code, data)
            return
        self._json(404, {"error": "not found"})
    def log_message(self, *a): pass

print("hh-sidecar on 127.0.0.1:8099", flush=True)
ThreadingHTTPServer(("127.0.0.1", 8099), H).serve_forever()
