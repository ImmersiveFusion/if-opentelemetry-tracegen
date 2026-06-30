# Where TraceGen Runs

**One container. 5.7 MB. It runs anywhere — and this is the board of where it actually does.**

TraceGen ships as a single distroless, multi-arch image (`linux/amd64` + `linux/arm64`), no Compose, no multi-gigabyte RAM footprint, no microservices to stand up:

```bash
# -insecure = plaintext gRPC for a local collector (skips TLS); drop it for a remote, authenticated endpoint
docker run --rm immersivefusion/tracegen -insecure -endpoint host.docker.internal:4317
```

That portability is the whole point — the same image runs on a laptop, a CI runner, a cloud cluster, or a fanless desktop in someone's office. Below is where it's running for real. If you're running it somewhere, add yours — see [Add your deployment](#add-your-deployment).

---

## The deployments

### Immersive Fusion — live public demo grids

- **Where it runs:** the **Immersive Fusion cloud** — seven always-on workloads on Kubernetes, deployments managed declaratively via GitOps (Argo CD).
- **What for:** **seven always-on public demo grids** — each one a separate TraceGen deployment streaming live OpenTelemetry traces into [IAPM](https://immersivefusion.com)'s 3D player, so anyone can open it and watch *real data moving* instead of a canned recording. Each grid is tuned to tell one story:
  - **traditional-clean** — a healthy e-commerce platform: the calm, mostly-green reference graph.
  - **ai-flagship** — an AI-native application, observed: a RAG pipeline, an AI chatbot, content moderation, and a multi-step agent emitting full OTel GenAI spans.
  - **blended** — the real world, traditional + AI together: classic microservices and AI services sharing one trace graph, with real error traffic.
  - **phantom** — services you didn't know you had: dead consumers, so the platform infers the missing ("phantom") services from the topology.
  - **ai-clean** — AI on a calm day: the agentic topology at a low error rate.
  - **traditional** — when the AI tier goes down: the full traditional platform with its AI backends erroring (rate limits, timeouts).
  - **incident** — everything is on fire: maximum errors plus dead consumers, the root-cause / incident-response demo.
- **Flavor:** the distroless container (`immersivefusion/tracegen`, pinned by digest in GitOps — the same pin-by-digest we'd ask of you below), one Kubernetes Deployment per grid, each shipping to `otlp.iapm.app:443`. The three densest grids (ai-clean, traditional, incident) run at a low `-level` floor — full topology, gentle trace rate — so the 3D player stays smooth while all seven run side by side.
- **The point:** the whole image is 5.7 MB and each grid sips a few dozen megabytes of RAM — light enough that the entire fleet would cheerfully run off a Mac mini in a broom closet. (It doesn't — it runs in the Immersive Fusion cloud — but it absolutely could.) **See them live — in 3D.** The way to experience this is the **IAPM 3D client**: install it, open a demo grid, and walk the live traces in three dimensions — fly the topology, drill into a failing span, lean on the AI assistant. That's the real thing. On a phone, or can't install right now? **IAPM Web** runs the same grids in your browser at [portal.iapm.app](https://portal.iapm.app) — sign in and open a grid. Don't want to install or sign in at all? **Watch the grids live on Twitch** at [twitch.tv/immersivefusion](https://www.twitch.tv/immersivefusion) — the 3D player, streamed in real time, zero friction.

---

## Add your deployment

Running TraceGen somewhere — a load test, a CI pipeline, a teaching lab, a backend bake-off, a homelab, a demo of your own? **List your shit too.** This board is earned, not bought: the only entry fee is that you actually run it.

Open a pull request at [github.com/ImmersiveFusion/if-opentelemetry-tracegen](https://github.com/ImmersiveFusion/if-opentelemetry-tracegen) adding a block under [The deployments](#the-deployments) using this template:

```markdown
### <Your name or org> — <one-line what>

- **Where it runs:** <platform + architecture, e.g. "AWS EKS, x86_64" or "a Raspberry Pi cluster">
- **What for:** <the use case — what TraceGen is doing for you>
- **Flavor:** <container or binary; the image tag you run, e.g. immersivefusion/tracegen:0.6.1; roughly how many instances / how hard you push it>
- **Link:** <optional but encouraged — a live URL, a blog post, or a repo; it's the best proof>
```

Keep it factual and specific — the specifics are the merit. No marketing, no logos-for-sale; just where the image lands and what it does there. A maintainer will review and merge; entries that name a platform, a use case, and a version (or a link) move fastest.

Prefer not to write the PR yourself? [Open an issue](https://github.com/ImmersiveFusion/if-opentelemetry-tracegen/issues/new) with the same details and we'll add it.

---

Contributions are accepted under the repository's [Apache-2.0 license](LICENSE).
