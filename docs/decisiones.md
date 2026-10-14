# Decisiones de la Fase 0

## Módulo Go único

El monorepo comienza con un solo módulo Go raíz y un `go.work` que lo referencia.
Es la opción más sencilla para compartir código localmente y evita introducir
versionado y reemplazos entre módulos antes de que existan servicios.

## PostgreSQL local

Docker Compose usa PostgreSQL 16 Alpine sin extensión TimescaleDB. Para esta
fase basta con validar la conectividad y persistencia local; la estrategia de
almacenamiento de mediciones se decidirá al implementar ese servicio.

Compose permite configurar nombre de base de datos, usuario, contraseña y
puerto mediante variables de entorno. Los valores de respaldo son credenciales
ficticias exclusivamente para desarrollo local. `.env.example` no contiene
secretos reales.

## Alcance del compose inicial

El compose levanta únicamente la base de datos. Los servicios de aplicación se
añadirán en sus fases correspondientes para que cada uno tenga una
implementación ejecutable antes de incluirlo en el despliegue local.

## Contratos gRPC de la Fase 1

Los contratos usan el paquete versionado `voltbarrio.v1` y generan Go bajo un
único paquete de importación del monorepo. El código generado va a `gen/go/` y
no se considera fuente de verdad: los `.proto` son la fuente canónica.

Las cantidades de energía se expresan en Wh y los precios en microcréditos por
kWh como enteros para evitar errores de redondeo binario. Los instantes se
representan con `google.protobuf.Timestamp`. Las operaciones mutables incluyen
identificadores estables de idempotencia en sus solicitudes.

Para mediciones se elige Ed25519: cada lectura tiene un identificador de clave
y una firma sobre la serialización determinista protobuf del mensaje `MeterReading`
contenido en el sobre firmado. El mecanismo de gestión/rotación de claves se
definirá al implementar el servicio.

El libro de auditoría usa SHA-256 para enlazar registros mediante el hash del
registro anterior. El precio de cierre se expone una sola vez por subasta y se
propaga al lote validado; las operaciones individuales no llevan precios
independientes.

La generación usa plugins remotos fijados a versiones en `buf.gen.yaml`; por
ello, ejecutar `make proto` requiere conectividad al Buf Schema Registry, pero
no credenciales ni servicios de pago.

## Subasta de la Fase 2

La subasta ordena compras por precio descendente y ventas por precio ascendente.
Ante precios iguales usa el instante de creación y luego el identificador de
orden, para que el resultado sea reproducible. Cruza órdenes mientras la
compra marginal cubra la venta marginal. El precio único es el punto medio
entero (redondeado hacia abajo) de esos límites marginales en la última
operación ejecutada; una subasta sin operaciones tiene precio cero.

El núcleo actual mantiene órdenes y resultados en memoria para hacer
demostrable la lógica idempotente y la liquidación de cantidades. La
persistencia PostgreSQL y los adaptadores gRPC quedan para sus fases
correspondientes.

Al confirmar una subasta se expiran las cantidades no ejecutadas y se cierra
el intervalo para nuevas órdenes; los reintentos de una orden ya aceptada
siguen devolviendo el resultado original.

## Red y control de la Fase 3

Cuando la energía propuesta excede el límite, cada operación recibe su cuota
proporcional en Wh enteros. Los residuos fraccionarios se asignan de mayor a
menor y los empates por `trade_id`; así el total aceptado es exactamente el
límite y ninguna operación recibe más energía de la propuesta. Sin exceso, se
conserva la energía original.

Se valida un solo lote por intervalo, coherente con la única subasta del
intervalo; un segundo lote con otro identificador se rechaza en lugar de
aplicar por error el límite completo dos veces.

El canal local de control asocia cada comando a un dispositivo y exige que su
deadline esté dentro del intervalo de cinco minutos. Las confirmaciones y los
comandos usan identificadores idempotentes; el consumidor del stream obtiene
un estado explícito de expiración si no llega una confirmación a tiempo. El
contrato ya define el RPC bidireccional; el adaptador gRPC queda pendiente de
generar los stubs Buf en un entorno con las herramientas instaladas.

## Liquidación y auditoría de la Fase 4

El ledger registra importes firmados en microcréditos: débitos positivos y
créditos negativos. Cada settlement requiere al menos dos asientos distintos
y suma exacta cero; se rechaza `MinInt64` para que cada asiento pueda revertirse
sin desbordamiento. Una compensación añade asientos opuestos, nunca modifica ni
elimina los originales. PostgreSQL refuerza el balance con un trigger diferido
y protege las filas del ledger contra `UPDATE` y `DELETE`.

La saga coordina puertos idempotentes de emparejamiento y pago local; no usa
proveedores ni credenciales externas. Ante un fallo después de confirmar una
etapa intenta la compensación correspondiente. El ledger PostgreSQL usa
transacciones; las pruebas unitarias usan un almacén en memoria. El estado de
la saga aún no se persiste: tras un corte de proceso, la recuperación depende
de reintentos con los mismos IDs idempotentes y no existe reconciliación
automática de operaciones externas.

SHA-256 cubre, en orden y con longitudes prefijadas, secuencia, identificador,
tipo, actor, payload, segundos/nanosegundos UTC con precisión de microsegundos
(la precisión persistida por PostgreSQL) y hash anterior. El primer
registro enlaza con 32 bytes cero. La verificación recorre secuencia y hashes
desde el inicio. Los scripts SQL se montan en el directorio de inicialización
de PostgreSQL de Compose y se aplican al crear un volumen nuevo.

## Medición y borde de la Fase 5

Las lecturas se firman con Ed25519 sobre la serialización protobuf
determinista del mensaje `MeterReading`; el `key_id` se vincula a un hogar en
configuración, evitando que una clave válida suplante otro hogar. PostgreSQL
impone idempotencia por `reading_id`, conserva el payload canónico y guarda
intentos rechazados. Las lecturas aceptadas y la bitácora de intentos son
append-only. Un límite configurable por hogar se aplica tras verificar firma.

La ruta REST usa batch unario porque el transcoder de Envoy no traduce el
client-streaming a JSON REST. Envoy añade rol `neighbor` y se autentica ante
medición por mTLS; el backend también exige rol por RPC. Los interceptores
compartidos cubren logging estructurado, contadores/duración Prometheus,
autorización, recuperación de pánicos, request-id y paso del contexto/deadline
a llamadas salientes. El gateway es la frontera de confianza local; la
autorización por metadatos no sustituye vincular identidades a certificados
en una instalación de producción.

Compose genera CA y certificados de desarrollo automáticamente en un volumen
local. Las claves no se almacenan en Git; al ser una demostración local, los
certificados no son aptos para producción. Buf usa plugins locales para no
enviar contratos a un servicio externo; `make proto` resuelve la dependencia
de anotaciones HTTP de Google y genera el descriptor consumido por Envoy.

La publicación de la demo separa el sitio estático en Vercel del microservicio
de medición y PostgreSQL en Render. En Render el gateway REST y el servidor
gRPC viven en el mismo contenedor; el gRPC queda ligado a loopback y no
expuesto, mientras Render termina HTTPS público. Esto evita mTLS entre
contenedores en esa demo, pero no reemplaza la topología mTLS de Compose ni
significa que el resto de la plataforma esté desplegado.
