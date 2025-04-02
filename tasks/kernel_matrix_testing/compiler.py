from __future__ import annotations

import json
import os
import sys
import tempfile
from pathlib import Path
from typing import TYPE_CHECKING, cast

import yaml
from invoke.context import Context
from invoke.runners import Result

from tasks.kernel_matrix_testing.tool import Exit, info, warn
from tasks.libs.ciproviders.gitlab_api import ReferenceTag
from tasks.libs.types.arch import ARCH_AMD64, ARCH_ARM64, Arch

if TYPE_CHECKING:
    from tasks.kernel_matrix_testing.types import PathOrStr


CONTAINER_AGENT_PATH = "/tmp/datadog-agent"

DOCKER_BASE_IMAGES = {
    "x64": "registry.ddbuild.io/ci/datadog-agent-buildimages/linux-glibc-2-17-x64",
    "arm64": "registry.ddbuild.io/ci/datadog-agent-buildimages/linux-glibc-2-23-arm64",
}

APT_URIS = {"amd64": "http://archive.ubuntu.com/ubuntu/", "arm64": "http://ports.ubuntu.com/ubuntu-ports/"}


def get_build_image_suffix_and_version() -> tuple[str, str]:
    gitlab_ci_file = Path(__file__).parent.parent.parent / ".gitlab-ci.yml"
    yaml.SafeLoader.add_constructor(ReferenceTag.yaml_tag, ReferenceTag.from_yaml)
    with open(gitlab_ci_file) as f:
        ci_config = yaml.safe_load(f)

    ci_vars = ci_config['variables']
    return ci_vars['DATADOG_AGENT_BUILDIMAGES_SUFFIX'], ci_vars['DATADOG_AGENT_BUILDIMAGES']


def get_docker_image_name(ctx: Context, container: str) -> str:
    res = ctx.run(f"docker inspect \"{container}\"", hide=True)
    if res is None or not res.ok:
        raise ValueError(f"Could not get {container} info")

    data = json.loads(res.stdout)
    return data[0]["Config"]["Image"]


class CompilerImage:
    def __init__(self, ctx: Context, arch: Arch):
        self.ctx = ctx
        self.arch: Arch = arch

    @property
    def name(self):
        return f"kmt-compiler-{self.arch.name}"

    @property
    def image(self):
        suffix, version = get_build_image_suffix_and_version()

        return f"{DOCKER_BASE_IMAGES[self.arch.ci_arch]}{suffix}:{version}"

    def _check_container_exists(self, allow_stopped=False):
        if self.ctx.config.run["dry"]:
            warn(f"[!] Dry run, not checking if compiler {self.name} is running")
            return True

        args = "a" if allow_stopped else ""
        res = self.ctx.run(f"docker ps -{args}qf \"name={self.name}\"", hide=True)
        if res is not None and res.ok:
            return res.stdout.rstrip() != ""
        return False

    @property
    def is_running(self):
        return self._check_container_exists(allow_stopped=False)

    @property
    def is_loaded(self):
        return self._check_container_exists(allow_stopped=True)

    def ensure_running(self):
        if not self.is_running:
            info(f"[*] Compiler for {self.arch} not running, starting it...")
            try:
                self.start()
            except Exception as e:
                raise Exit(f"Failed to start compiler for {self.arch}: {e}") from e

    def ensure_version(self):
        if not self.is_loaded:
            return  # Nothing to do if the container is not loaded

        image_used = get_docker_image_name(self.ctx, self.name)
        if image_used != self.image:
            warn(f"[!] Running compiler image {image_used} is different from the expected {self.image}, will restart")
            self.start()

    def exec(
        self,
        cmd: str,
        user="compiler",
        verbose=True,
        run_dir: PathOrStr | None = None,
        allow_fail=False,
        force_color=True,
    ):
        if run_dir:
            cmd = f"cd {run_dir} && {cmd}"

        self.ensure_running()
        color_env = "-e FORCE_COLOR=1"
        if not force_color:
            color_env = ""

        # Set FORCE_COLOR=1 so that termcolor works in the container
        return self.ctx.run(
            f"docker exec -u {user} -i {color_env} {self.name} bash -l -c \"{cmd}\"",
            hide=(not verbose),
            warn=allow_fail,
        )

    def stop(self) -> Result:
        res = self.ctx.run(f"docker rm -f $(docker ps -aqf \"name={self.name}\")")
        return cast('Result', res)  # Avoid mypy error about res being None

    def start(self) -> None:
        if self.is_loaded:
            self.stop()

        # Check if the image exists
        res = self.ctx.run(f"docker image inspect {self.image}", hide=True, warn=True)
        if res is None or not res.ok:
            info(f"[!] Image {self.image} not found, pulling...")
            self.ctx.run(f"docker pull {self.image}")

        platform = ""
        if self.arch != Arch.local():
            platform = f"--platform linux/{self.arch.go_arch}"
        res = self.ctx.run(
            f"docker run {platform} -d --restart always --name {self.name} "
            f"--mount type=bind,source={os.getcwd()},target={CONTAINER_AGENT_PATH} "
            f"{self.image} sleep \"infinity\"",
            warn=True,
        )
        if res is None or not res.ok:
            raise ValueError(f"Failed to start compiler container {self.name}")

        # Due to permissions issues, we do not want to compile with the root user in the Docker image. We create a user
        # inside there with the same UID and GID as the current user
        uid = cast('Result', self.ctx.run("id -u")).stdout.rstrip()
        gid = cast('Result', self.ctx.run("id -g")).stdout.rstrip()

        if uid == 0:
            # If we're starting the compiler as root, we won't be able to create the compiler user
            # and we will get weird failures later on, as the user 'compiler' won't exist in the container
            raise ValueError("Cannot start compiler as root, we need to run as a non-root user")

        # Now create the compiler user with same UID and GID as the current user
        self.exec(f"getent group {gid} || groupadd -f -g {gid} compiler", user="root")
        self.exec(f"getent passwd {uid} || useradd -m -u {uid} -g {gid} compiler", user="root")

        if sys.platform != "darwin":  # No need to change permissions in MacOS
            self.exec(
                f"chown {uid}:{gid} {CONTAINER_AGENT_PATH} && chown -R {uid}:{gid} {CONTAINER_AGENT_PATH}", user="root"
            )

        cross_arch = ARCH_ARM64 if self.arch == ARCH_AMD64 else ARCH_AMD64
        self.exec("chmod a+rx /root", user="root")  # Some binaries will be in /root and need to be readable
        self.exec(f"dpkg --add-architecture {cross_arch.go_arch}", user="root")
        with tempfile.NamedTemporaryFile(mode='w') as sources:
            sources.write(get_apt_sources(self.arch))
            sources.write(get_apt_sources(cross_arch))
            sources.flush()
            self.ctx.run(f"docker cp {sources.name} {self.name}:/etc/apt/sources.list.d/ubuntu.sources")

        self.exec("apt-get update", user="root")
        res = self.exec("dpkg-query -l | grep linux-headers-.*-generic | awk '{ print \\$2 }'", user="root")
        headers_package = res.stdout.strip()
        self.exec(
            f"mv /usr/src/{headers_package}/include/generated/*.h /usr/src/{headers_package}/arch/{self.arch.kernel_arch}/include/generated/",
            user="root",
        )

        res = self.exec(
            f"apt-get download {headers_package}:{cross_arch.go_arch} --print-uris | awk '{{ print \\$2 }}'",
            user="root",
        )
        headers_package_filename = res.stdout.strip()
        self.exec(f"apt-get download {headers_package}:{cross_arch.go_arch}", user="root")

        # Uncompress the package in the root directory, so that we have access to the headers
        # We cannot install because the architecture will not match
        # Extract into a .tar file and then use tar to extract the contents to avoid issues
        # with dpkg-deb not respecting symlinks.
        self.exec(f"dpkg-deb --fsys-tarfile {headers_package_filename} > {headers_package_filename}.tar", user="root")
        self.exec(f"tar --skip-old-files -xf {headers_package_filename}.tar -C /", user="root")
        self.exec(
            f"mv /usr/src/{headers_package}/include/generated/*.h /usr/src/{headers_package}/arch/{cross_arch.kernel_arch}/include/generated/",
            user="root",
        )

        self.exec("apt-get install -y --no-install-recommends sudo", user="root")
        self.exec("usermod -aG sudo compiler && echo 'compiler ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers", user="root")
        self.exec(f"cp /root/.bashrc /home/compiler/.bashrc && chown {uid}:{gid} /home/compiler/.bashrc", user="root")
        self.exec("mkdir ~/.cargo && touch ~/.cargo/env", user="compiler")
        self.exec("dda self telemetry disable", user="compiler", force_color=False)
        self.exec(f"install -d -m 0777 -o {uid} -g {uid} /go", user="root")
        self.exec(
            f"echo export DD_CC=/opt/toolchains/{self.arch.gcc_arch}/bin/{self.arch.gcc_arch}-unknown-linux-gnu-gcc >> /home/compiler/.bashrc",
            user="compiler",
        )
        self.exec(
            f"echo export DD_CXX=/opt/toolchains/{self.arch.gcc_arch}/bin/{self.arch.gcc_arch}-unknown-linux-gnu-g++ >> /home/compiler/.bashrc",
            user="compiler",
        )
        self.exec(
            f"echo export DD_CC_CROSS=/opt/toolchains/{cross_arch.gcc_arch}/bin/{cross_arch.gcc_arch}-unknown-linux-gnu-gcc >> /home/compiler/.bashrc",
            user="compiler",
        )
        self.exec(
            f"echo export DD_CXX_CROSS=/opt/toolchains/{cross_arch.gcc_arch}/bin/{cross_arch.gcc_arch}-unknown-linux-gnu-g++ >> /home/compiler/.bashrc",
            user="compiler",
        )


def get_apt_sources(arch: Arch) -> str:
    apt_uri = APT_URIS[arch.go_arch]
    return f"""
Types: deb
URIs: {apt_uri}
Suites: noble noble-updates noble-backports
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
Architectures: {arch.go_arch}

Types: deb
URIs: {apt_uri}
Suites: noble-security
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
Architectures: {arch.go_arch}

"""


def get_compiler(ctx: Context):
    cc = CompilerImage(ctx, Arch.local())
    cc.ensure_version()
    cc.ensure_running()

    return cc
