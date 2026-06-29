package adminweb

import "net/http"

// esCatalog maps the English source string (the key) to its Spanish rendering.
// English is the identity locale (no catalog lookup); a key absent here renders as
// English, which the completeness check treats as a bug rather than a silent leak.
// Keep this exhaustive over every key passed to translate / {{T}}. An entry MAY
// equal its key (an intentional passthrough like "Slug", "UTC", "YouTube"), but it
// MUST be present.
//
// Some entries deliberately are NOT segment-for-segment translations of their key:
// where a sentence wraps a literal <code> or <a> token, the key and value are each
// phrased so the language reads correctly with the literal token spliced into the
// English word position (see the "...is not configured..." notices and the prose
// that wraps the Regenerate link).
var esCatalog = map[string]string{
	// ----- shell / chrome -----
	"Admin Panel":   "Panel de administración",
	"Control panel": "Panel de control",
	"Primary":       "Principal",
	"Sections":      "Secciones",
	"Theme":         "Tema",
	"Language":      "Idioma",
	"System":        "Sistema",
	"Light":         "Claro",
	"Dark":          "Oscuro",
	"Log out":       "Cerrar sesión",

	// ----- nav -----
	"Dashboard":  "Artículos",
	"Create":     "Crear",
	"Authors":    "Autores",
	"Topics":     "Temas",
	"Audit":      "Auditoría",
	"Regenerate": "Regenerar",

	// ----- login -----
	"Operator token": "Token de operador",
	"Sign in":        "Iniciar sesión",
	"Invalid token.": "Token inválido.",

	// ----- browse filters -----
	"Filter":             "Filtro",
	"Search":             "Buscar",
	"title or body text": "título o texto del cuerpo",
	"Section":            "Sección",
	"none":               "ninguno",
	"Author":             "Autor",
	"Topic":              "Tema",
	"From":               "Desde",
	"To (inclusive)":     "Hasta (inclusive)",
	"Order":              "Orden",
	"Newest first":       "Más recientes primero",
	"Oldest first":       "Más antiguos primero",
	"Apply":              "Aplicar",
	"Reset":              "Restablecer",
	"Active filters":     "Filtros activos",
	"Remove":             "Quitar",
	// chip axis labels (dynamic .Axis values, lowercase by design)
	"section": "sección",
	"author":  "autor",
	"topic":   "tema",
	"search":  "búsqueda",
	"from":    "desde",
	"to":      "hasta",

	// ----- results table -----
	"article":                                "artículo",
	"articles":                               "artículos",
	"showing":                                "mostrando",
	"Title":                                  "Título",
	"Published":                              "Publicado",
	"Prev":                                   "Anterior",
	"Next":                                   "Siguiente",
	"Page":                                   "Página",
	"of":                                     "de",
	"Pagination":                             "Paginación",
	"No articles match the current filters.": "Ningún artículo coincide con los filtros actuales.",

	// ----- detail -----
	"Back to browse": "Volver a la lista",
	"By":             "Por",
	"in":             "en",
	"on":             "el",
	"This article is removed (hidden from the public site).": "Este artículo está eliminado (oculto del sitio público).",
	"Edit":         "Editar",
	"Restore":      "Restaurar",
	"Delete":       "Eliminar",
	"Metadata":     "Metadatos",
	"Content hash": "Hash de contenido",
	"Created":      "Creado",

	// ----- audit -----
	"Audit log unavailable":                              "Registro de auditoría no disponible",
	"The audit log is not configured for this instance.": "El registro de auditoría no está configurado en esta instancia.",
	"Audit filter":                                       "Filtro de auditoría",
	"exact author":                                       "autor exacto",
	"Showing":                                            "Mostrando",
	"submission":                                         "envío",
	"submissions":                                        "envíos",
	"page":                                               "página",
	"Slug":                                               "Slug",
	"Scopes":                                             "Alcances",
	"Idempotency key":                                    "Clave de idempotencia",
	"No submissions recorded for the current filters.": "No hay envíos registrados para los filtros actuales.",

	// ----- regenerate -----
	"Regenerate static site": "Regenerar el sitio estático",
	"Rebuild the public static site from the current store. Only changed artifacts are written; the purge list reports every URL to invalidate at the edge.": "Reconstruye el sitio estático público a partir del almacén actual. Solo se escriben los artefactos que cambiaron; la lista de purga informa cada URL a invalidar en el borde.",
	"Regeneration is not configured for this instance. Set the output directory and base URL to enable it.":                                                  "La regeneración no está configurada en esta instancia. Defina el directorio de salida y la URL base para habilitarla.",
	"Regeneration failed":   "Falló la regeneración",
	"Regeneration complete": "Regeneración completada",
	"Written":               "Escritos",
	"Unchanged":             "Sin cambios",
	"Deleted":               "Eliminados",
	"Purge URLs":            "URLs a purgar",
	"No URLs to purge.":     "No hay URLs para purgar.",

	// ----- create -----
	"New article": "Nuevo artículo",
	"Publishes through the write API, the same boundary the author agents use, so the admin never writes the database directly. After it lands, run": "Publica a través de la API de escritura, el mismo límite que usan los agentes autores, por lo que el panel nunca escribe directamente en la base de datos. Cuando quede registrado, ejecute",
	"to push it to the public site.":                             "para publicarlo en el sitio público.",
	"Manual publishing is not configured for this instance. Set": "La publicación manual no está configurada en esta instancia. Defina",
	"and":                          "y",
	"(an operator key holding the": "(una clave de operador con el alcance",
	"scope) to enable it.":         ") para habilitarla.",
	"Article published":            "Artículo publicado",
	"Already published (idempotent, no new article)": "Ya estaba publicado (idempotente, sin artículo nuevo)",
	"View in admin":                    "Ver en el panel",
	"It is in the store now. Run":      "Ya está en el almacén. Ejecute",
	"to publish it to the live site.":  "para publicarlo en el sitio en vivo.",
	"(comma-separated, optional)":      "(separados por comas, opcional)",
	"Published at":                     "Fecha de publicación",
	"(optional, UTC; blank means now)": "(opcional, UTC; vacío = ahora)",
	"Body":                             "Cuerpo",
	"(Markdown; raw HTML is stripped)": "(Markdown; se elimina el HTML directo)",
	"Media":                            "Medios",
	"(optional)":                       "(opcional)",
	"Upload image":                     "Subir imagen",
	"Stored on this site and attached as the article image. Max 10 MiB; JPEG, PNG, GIF, or WebP.": "Se almacena en este sitio y se adjunta como imagen del artículo. Máximo 10 MiB; JPEG, PNG, GIF o WebP.",
	"Image URL":                  "URL de imagen",
	"(or paste an existing one)": "(o pegue una existente)",
	"Image alt text":             "Texto alternativo de la imagen",
	"YouTube":                    "YouTube",
	"(URL or video id; takes precedence over the image)": "(URL o ID de video; tiene prioridad sobre la imagen)",
	"(optional JSON object)":                             "(objeto JSON opcional)",
	"Publish article":                                    "Publicar artículo",

	// ----- field / validation errors (English keys set in Go, rendered via {{T .}}) -----
	"required":                                                      "obligatorio",
	"must be a https URL or a /media path":                          "debe ser una URL https o una ruta /media",
	"not a recognized YouTube URL or video id":                      "no es una URL ni un ID de video de YouTube válido",
	"use YYYY-MM-DD, a datetime-local value, or RFC 3339":           "use YYYY-MM-DD, un valor datetime-local o RFC 3339",
	`must be a JSON object, e.g. {"image":"https://example/x.jpg"}`: `debe ser un objeto JSON, p. ej. {"image":"https://example/x.jpg"}`,
	"image is too large (max 10 MiB)":                               "la imagen es demasiado grande (máximo 10 MiB)",
	"could not read the uploaded image":                             "no se pudo leer la imagen subida",
	"handle is required":                                            "el identificador es obligatorio",
	"slug is required":                                              "el slug es obligatorio",
	"not found (it may have already been removed)":                  "no encontrado (puede que ya se haya eliminado)",
	"this edit collides with an existing article (identical content); change the title or body": "esta edición coincide con un artículo existente (contenido idéntico); cambie el título o el cuerpo",

	// ----- authors registry -----
	"The managed author registry: the canonical identity the public site renders. Changes go through the operator API, the same write boundary the agents use, so the admin never writes the database directly. Run": "El registro de autores gestionados: la identidad canónica que muestra el sitio público. Los cambios pasan por la API de operador, el mismo límite de escritura que usan los agentes, por lo que el panel nunca escribe directamente en la base de datos. Ejecute",
	"after a change to push it live.": "después de un cambio para publicarlo en vivo.",
	"The author registry is not configured for this instance. Set the operator API URL and an": "El registro de autores no está configurado en esta instancia. Defina la URL de la API de operador y un token de operador con",
	"operator token to enable it.": "para habilitarlo.",
	"New author":                   "Nuevo autor",
	"Show removed":                 "Mostrar eliminados",
	"Hide removed":                 "Ocultar eliminados",
	"Handle":                       "Identificador",
	"Name":                         "Nombre",
	"Bio":                          "Biografía",
	"Actions":                      "Acciones",
	"removed":                      "eliminado",
	"No authors yet.":              "Todavía no hay autores.",
	"Create one":                   "Cree uno",

	// ----- author form -----
	"Edit author":     "Editar autor",
	"Back to authors": "Volver a autores",
	"The author registry is not configured for this instance.":                                "El registro de autores no está configurado en esta instancia.",
	"The handle is the stable key and matches the author byline; it does not change on edit.": "El identificador es la clave estable y coincide con la firma del autor; no cambia al editar.",
	"Avatar URL":    "URL del avatar",
	"Save changes":  "Guardar cambios",
	"Create author": "Crear autor",

	// ----- topics registry -----
	"The managed topic registry: the canonical topics, with descriptions, the public site renders. Changes go through the operator API. Run": "El registro de temas gestionados: los temas canónicos, con descripciones, que muestra el sitio público. Los cambios pasan por la API de operador. Ejecute",
	"The topic registry is not configured for this instance. Set the operator API URL and an":                                                "El registro de temas no está configurado en esta instancia. Defina la URL de la API de operador y un token de operador con",
	"New topic":      "Nuevo tema",
	"Label":          "Etiqueta",
	"Description":    "Descripción",
	"No topics yet.": "Todavía no hay temas.",

	// ----- topic form -----
	"Edit topic":     "Editar tema",
	"Back to topics": "Volver a temas",
	"The topic registry is not configured for this instance.":                                              "El registro de temas no está configurado en esta instancia.",
	"The slug is the stable key and matches the topic in article memberships; it does not change on edit.": "El slug es la clave estable y coincide con el tema en las membresías de los artículos; no cambia al editar.",
	"Create topic": "Crear tema",

	// ----- article edit -----
	"Edit article":    "Editar artículo",
	"Back to article": "Volver al artículo",
	"Saving recomputes the content hash and rebuilds the page; the permalink (slug) never changes. Edits go through the operator API, so the admin never writes the database directly. Run": "Al guardar se vuelve a calcular el hash de contenido y se reconstruye la página; el enlace permanente (slug) nunca cambia. Las ediciones pasan por la API de operador, por lo que el panel nunca escribe directamente en la base de datos. Ejecute",
	"afterward to push it live.": "después para publicarlo en vivo.",
	"Article editing is not configured for this instance. Set the operator API URL and an":                                                                "La edición de artículos no está configurada en esta instancia. Defina la URL de la API de operador y un token de operador con",
	"This article is currently removed (hidden from the public site). Saving an edit keeps it removed; use Restore on the article page to bring it back.": "Este artículo está eliminado actualmente (oculto del sitio público). Guardar una edición lo mantiene eliminado; use Restaurar en la página del artículo para recuperarlo.",
	"(comma-separated)": "(separados por comas)",
	"(UTC)":             "(UTC)",

	// ----- Go-side page titles -----
	"Browse articles": "Explorar artículos",
	"Audit log":       "Registro de auditoría",
}

// translate returns the Spanish rendering of key when lang is "es" and the key is
// in the catalog; otherwise it returns key unchanged (English is identity, and an
// untranslated key falls back to English rather than failing).
func translate(lang, key string) string {
	if lang == "es" {
		if v, ok := esCatalog[key]; ok {
			return v
		}
	}
	return key
}

// localeFor resolves the active UI locale for a request: the admin_lang cookie
// wins when it holds a known value; otherwise the Handler default (es in prod,
// empty -> en in tests).
func (h *Handler) localeFor(r *http.Request) string {
	if c, err := r.Cookie("admin_lang"); err == nil && (c.Value == "en" || c.Value == "es") {
		return c.Value
	}
	if h.defaultLocale == "es" {
		return "es"
	}
	return "en"
}

// tr translates a Go-side string for this request's locale (page titles, login
// title, etc.).
func (h *Handler) tr(r *http.Request, key string) string {
	return translate(h.localeFor(r), key)
}
