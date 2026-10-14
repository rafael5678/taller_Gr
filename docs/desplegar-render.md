# Publicar la demo: Vercel y Render

El frontend y el backend quedan en plataformas distintas:

- Vercel publica el sitio de `web/`. `vercel.json` construye `web/dist` e
  inyecta la dirección del microservicio de medición.
- Render, con [`render.yaml`](../render.yaml), crea solo el microservicio
  `voltbarrio-medicion-demo`. No crea otra base. `DATABASE_URL` se pega a mano
  con la URL interna de `hospy_db`, la que ya usa Triaje. Las lecturas quedan
  en el esquema `voltbarrio`, aparte de las tablas del hospital.

Órdenes, emparejamiento, red, dispositivos, liquidación y auditoría siguen en
el repositorio, pero todavía no tienen un servidor ejecutable. Esta publicación
comprueba el microservicio de medición.

## Frontend en Vercel

1. En Vercel, importa `https://github.com/rafael5678/taller_Gr`.
2. El proyecto debe llamarse `voltbarrio` para que el sitio quede en
   `https://voltbarrio.vercel.app`. Ese origen ya está permitido por el
   backend en `FRONTEND_ORIGIN`.
3. No cambies el comando de build ni el directorio de salida: los toma de
   `vercel.json`.
4. Si Render asigna otro host al backend, define en Vercel la variable
   `API_BASE_URL` con la URL `https://` completa y vuelve a desplegar.

## Backend en Render

1. El código ya está en GitHub. No agregues `.env`, certificados ni llaves
   privadas.
2. Abre [New Blueprint](https://dashboard.render.com/blueprint/new?repo=https://github.com/rafael5678/taller_Gr)
   y confirma que Render detecte `render.yaml` en la raíz.
3. En `hospy_db`, abre **Connect** y copia la **Internal Database URL**.
   En el blueprint, pega esa URL en `DATABASE_URL`. En
   `MEASUREMENT_PUBLIC_KEYS` pega `{}`. Revisa cualquier aviso de costos y
   elige **Apply** o **Manual sync**. El primer build compila Go y genera los
   stubs protobuf dentro de la imagen.
4. Espera a que el backend indique **Live** y responda en `/healthz`. Si el
   sitio de Vercel no queda en `https://voltbarrio.vercel.app`, cambia
   `FRONTEND_ORIGIN` por ese origen exacto, incluido `https://`, y redepliega.
5. Abre el frontend. Genera una llave Ed25519 y copia el JSON público.
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
