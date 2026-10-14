# VoltBarrio

VoltBarrio es un caso de estudio académico ficticio sobre una microrred
comunitaria con intercambio de energía entre hogares. El sistema se construirá
por fases como un monorepo Go con contratos Protocol Buffers y servicios gRPC.

## Estado

Fase 5: además de los núcleos de fases anteriores, medición Ed25519 con
idempotencia PostgreSQL y registro de firmas rechazadas, RPC gRPC de streaming,
batch REST por Envoy, interceptores compartidos, health check y mTLS de
desarrollo. La saga y el ledger/auditoría están implementados, pero aún no hay
servidores ejecutables para todos esos servicios.

En esta sesión no se han podido ejecutar generación Buf, compilación, pruebas,
Compose ni el build de Render porque Go, Buf, Make y Docker no están instalados
en el entorno actual.

## Requisitos locales

- Go 1.22 o posterior
- GNU Make
- Docker Compose v2
- Buf v2 (para `make proto`; los plugins de generación están fijados en
  `buf.gen.yaml` y deben estar instalados localmente como
  `protoc-gen-go` v1.36.6 y `protoc-gen-go-grpc` v1.5.1)

## Inicio de desarrollo

1. Copia `.env.example` a `.env` y ajusta los valores si es necesario.
2. Ejecuta `make up` para iniciar PostgreSQL.
3. Ejecuta `make down` para detener los contenedores. El volumen de datos se
   conserva.

Los valores incluidos son ficticios y solo sirven para desarrollo local; no
deben usarse en entornos compartidos o de producción. Compose genera una CA y
certificados efímeros de desarrollo; las claves privadas generadas tienen
permisos legibles dentro de la red local de contenedores para que el usuario
no privilegiado del servicio pueda leerlas. No son certificados de producción.
Para aceptar mediciones se debe configurar `MEASUREMENT_PUBLIC_KEYS` como un
objeto JSON cuya clave sea el `key_id` y cuyo valor contenga `household_id` y
`public_key` Ed25519 en Base64. Vacío significa que no hay medidores confiables.

## Comandos

- `make proto`: genera código a partir de los contratos Protocol Buffers.
- `make build`: compila los paquetes Go.
- `make test`: ejecuta las pruebas Go.
- `make up`: inicia los servicios definidos en Docker Compose.
- `make down`: detiene los servicios sin eliminar sus datos persistentes.

El batch REST de Envoy se publica en
`POST /v1/readings:batch`; el método gRPC streaming queda disponible para
medidores conectados directamente con un certificado de cliente confiable.
El rol HTTP se fija en el gateway como `neighbor`. La autorización por metadatos
asume que solo el gateway y servicios confiables llegan al backend; en
producción los roles deben vincularse a identidades/certificados autenticados.
El endpoint de métricas de medición queda en `:9100/`.

## Demo: frontend en Vercel y medición en Render

El sitio estático vive en Vercel. Render ejecuta solo el microservicio de
medición y su PostgreSQL, definidos en `render.yaml`. La guía está en
[`docs/desplegar-render.md`](docs/desplegar-render.md). Esta demo publica el
servicio de medición, no los demás microservicios del mercado.

La arquitectura y las decisiones iniciales se documentan en
`docs/decisiones.md`.
