"""Minimal Meridian HTTP client and temporary server lifecycle helper."""
import json
import select
import subprocess
from contextlib import contextmanager
from urllib.error import HTTPError
from urllib.request import Request, urlopen

PREFIX = "/meridian/v1"
IMAGE_MIME = "application/vnd.meridian.image"


class ApiError(Exception):
    def __init__(self, status, body):
        super().__init__(f"HTTP {status}: {body!r}")
        self.status = status
        self.body = body


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, headers=None, raw=False):
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            args["Content-Type"] = "application/json"
        req = Request(self.url + PREFIX + path, data=value, method=method, headers=args)
        try:
            response = urlopen(req, timeout=10)
        except HTTPError as error:
            response = error
        with response:
            payload = response.read()
            if response.status >= 400:
                try:
                    body = json.loads(payload)
                except json.JSONDecodeError:
                    body = payload.decode("utf-8", "replace")
                raise ApiError(response.status, body)
            if raw:
                return payload, response.headers
            return json.loads(payload), response.headers

    def get(self, path):
        return self.request("GET", path)[0]

    def post(self, path, value):
        return self.request("POST", path, value)[0]

    def create_layer(self, name, kind, bounds, exclusive):
        return self.post("/strata", dict(
            name=name, kind=kind, bounds=bounds, exclusive=exclusive))

    def edit(self, *items):
        return self.post("/edits", dict(edits=list(items)))

    def export_image(self):
        raw, headers = self.request("GET", "/image", raw=True)
        return raw, headers.get("Meridian-Revision")

    def restore_image(self, raw, revision):
        return self.request("PUT", "/image", raw,
                            {"Content-Type": IMAGE_MIME,
                             "If-Meridian-Revision": str(revision)})[0]

    def window(self, layer, bounds, limit=256):
        return self.post(f"/strata/{layer}/window", dict(bounds=bounds, limit=limit))

    def index_audit(self, layer):
        return self.get(f"/strata/{layer}/index-audit")


@contextmanager
def running_server(binary, data_dir):
    """Start meridian on a system-assigned port against an empty data dir."""
    args = [str(binary), "-listen", "127.0.0.1:0", "-data", str(data_dir)]
    child = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        readable, _, _ = select.select([child.stderr], [], [], 15)
        if not readable:
            raise RuntimeError("service did not start within 15 seconds")
        startup = json.loads(child.stderr.readline())
        if "address" not in startup:
            raise RuntimeError(startup)
        yield Session("http://" + startup["address"]), startup
    finally:
        if child.poll() is None:
            child.terminate()
        try:
            child.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.communicate(timeout=5)


def insert_extent(layer, feature_id, category, rect):
    x0, y0, x1, y1 = rect
    return dict(layer=layer, id=feature_id, mode="insert", value=dict(
        id=feature_id, **{"class": category},
        shape=dict(extent=dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1))))


def insert_mark(layer, feature_id, category, xy):
    x, y = xy
    return dict(layer=layer, id=feature_id, mode="insert", value=dict(
        id=feature_id, **{"class": category}, shape=dict(mark=dict(x=x, y=y))))
