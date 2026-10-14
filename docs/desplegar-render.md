# Publicar la demo en Render

El Blueprint [`render.yaml`](../render.yaml) crea tres recursos:

- `voltbarrio-medicion-demo`: backend Go de mediciones y REST.
- `voltbarrio-frontend`: sitio estático para firmar y enviar una lectura.
- `voltbarrio-demo-db`: PostgreSQL para mediciones aceptadas y rechazadas.

Esta publicación **no es el backend completo del mercado**: órdenes,
emparejamiento, red, dispositivos, liquidación y auditoría todavía no tienen
servidores ejecutables integrados. La demo solo comprueba el recorrido de una
lectura firmada.

## Publicación inicial

1. Sube el proyecto a un repositorio GitHub al que tengas acceso. No agregues
   `.env`, certificados ni llaves privadas.
2. En Render, elige **New → Blueprint**, conecta ese repositorio y confirma
   que Render detecte `render.yaml` en la raíz.
3. Render mostrará los servicios y la base PostgreSQL que va a crear. Si pide
   el valor inicial de `MEASUREMENT_PUBLIC_KEYS`, pega `{}` por ahora. Revisa
   cualquier aviso de costos y elige **Apply** para desplegarlos. El primer
   build compila Go y genera los stubs protobuf localmente dentro de la imagen.
4. Espera a que backend y frontend indiquen **Live**. El backend debe responder
   en `/healthz`. Si el dominio asignado al frontend no es
   `https://voltbarrio-frontend.onrender.com`, cambia la variable
   `FRONTEND_ORIGIN` del backend por el dominio exacto del sitio y redepliega.
5. Abre el frontend. Genera una llave Ed25519 y copia el JSON público mostrado.
   En Render abre el backend, **Environment**, y pega ese JSON en
   `MEASUREMENT_PUBLIC_KEYS`; guarda y espera el redeploy.
6. Descarga el archivo de llave privada que ofrece el navegador. Guárdalo en un
   lugar privado; no lo subas a Git ni lo pegues en Render. Cárgalo de nuevo en
   la página para firmar una lectura de prueba.

Render entrega HTTPS para las URL públicas. En esta demo REST y gRPC se
comunican por loopback dentro del mismo contenedor; el gRPC no se expone
públicamente. En Compose se mantiene mTLS entre Envoy y medición.

## Costos, límites y datos

El Blueprint solicita planes `free` donde Render los permite. La disponibilidad
y duración de PostgreSQL gratuito puede cambiar; comprueba el costo y las
condiciones que Render muestra **antes** de confirmar. Los servicios gratuitos
pueden suspenderse por inactividad y la base gratuita puede tener caducidad.
No cargues datos personales ni dependas de esta demo para operar una microrred
real. Configura una copia de seguridad antes de usar cualquier plan persistente.

## Si el deploy falla

- Revisa **Events** y **Logs** del backend; errores de generación suelen
  indicar dependencias Buf o plugins locales.
- Un backend que no pasa `/healthz` suele tener una conexión PostgreSQL
  incorrecta o no pudo aplicar `003_measurements.sql`.
- Si la página no llega al API, revisa que `API_BASE_URL` apunte al host del
  backend y que `FRONTEND_ORIGIN` coincida exactamente con el host del sitio,
  incluyendo `https://`.
- Si una firma se rechaza, verifica que el backend tenga desplegado el JSON
  público correspondiente al `key_id` y al mismo `household_id`.
